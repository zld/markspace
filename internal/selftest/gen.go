// Package selftest generates deterministic local sample media (MP4/MOV
// containers and RTP captures) and runs the processing pipeline over them.
// No external files or network access are involved; the same parameters
// always produce byte-identical samples and identical statistics.
package selftest

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/pion/rtp"

	"mediacheck/internal/adapter"
	"mediacheck/internal/clock"
	"mediacheck/internal/remux"
)

const (
	VideoTimescale = clock.Timescale(90000)
	AudioTimescale = clock.Timescale(48000)
	FrameDuration  = int64(3600) // 40ms @90kHz
	AudioFrameDur  = int64(960)  // 20ms @48kHz
	Frames         = 100         // 4s of video
	RTPClock       = clock.Timescale(90000)
	rtpDataPerPkt  = 1200
	seqBase        = uint16(1000)
)

// Generated is a deterministic synthetic media clip.
type Generated struct {
	Video      []adapter.Sample
	Audio      []adapter.Sample
	VideoData  [][]byte
	AudioData  [][]byte
	DurationNs int64
}

// splitmix64 is a deterministic content hash for sample sizes/payloads.
func splitmix64(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

func payload(kind, index, size int) []byte {
	b := make([]byte, size)
	seed := splitmix64(uint64(kind)<<32 | uint64(index))
	for i := range b {
		if i%8 == 0 {
			seed = splitmix64(seed)
		}
		b[i] = byte(seed >> (8 * (i % 8)))
	}
	return b
}

// GenerateMedia builds a clip whose payload bitrate matches targetKbps.
// When withAudio is false the whole budget goes to the video track.
func GenerateMedia(targetKbps float64, withAudio bool) *Generated {
	const seconds = Frames * 40 / 1000
	totalBytes := int64(targetKbps*1000) * int64(seconds) / 8
	const overhead = 4096 // container tables estimate
	totalBytes -= overhead

	g := &Generated{DurationNs: int64(seconds) * 1_000_000_000}
	audioFrames := Frames * 2
	audioTotal := int64(0)
	if withAudio {
		audioTotal = totalBytes / 10
	}
	videoTotal := totalBytes - audioTotal

	// Video frames: keyframes (every 25) weigh 3x, plus deterministic jitter.
	weights := make([]float64, Frames)
	var wsum float64
	for i := range weights {
		w := 1.0
		if i%25 == 0 {
			w = 3.0
		}
		h := splitmix64(uint64(i))
		w *= 0.85 + 0.30*float64(h%1000)/1000.0
		weights[i] = w
		wsum += w
	}
	var vacc int64
	for i := 0; i < Frames; i++ {
		size := int64(float64(videoTotal) * weights[i] / wsum)
		if i == Frames-1 {
			size = videoTotal - vacc
		}
		vacc += size
		g.Video = append(g.Video, adapter.Sample{
			DTS: int64(i) * FrameDuration, CTS: int64(i) * FrameDuration,
			Duration: FrameDuration, Size: uint32(size), Keyframe: i%25 == 0,
		})
		g.VideoData = append(g.VideoData, payload(1, i, int(size)))
	}
	if withAudio {
		per := audioTotal / int64(audioFrames)
		for i := 0; i < audioFrames; i++ {
			size := per
			if i == audioFrames-1 {
				size = audioTotal - per*int64(audioFrames-1)
			}
			g.Audio = append(g.Audio, adapter.Sample{
				DTS: int64(i) * AudioFrameDur, CTS: int64(i) * AudioFrameDur,
				Duration: AudioFrameDur, Size: uint32(size), Keyframe: true,
			})
			g.AudioData = append(g.AudioData, payload(2, i, int(size)))
		}
	}
	return g
}

// WriteMP4 writes the clip to path through the remuxer.
func WriteMP4(path string, g *Generated) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	specs := []remux.TrackSpec{
		{ID: 1, Kind: adapter.KindVideo, Timescale: VideoTimescale, Samples: g.Video},
	}
	data := [][][]byte{g.VideoData}
	if len(g.Audio) > 0 {
		specs = append(specs, remux.TrackSpec{
			ID: 2, Kind: adapter.KindAudio, Timescale: AudioTimescale, Samples: g.Audio,
		})
		data = append(data, g.AudioData)
	}
	w := remux.New(f)
	if err := w.WriteHeader(specs, g.DurationNs); err != nil {
		return err
	}
	for ti := range specs {
		for _, d := range data[ti] {
			if err := w.WriteSample(ti, d); err != nil {
				return err
			}
		}
	}
	_, err = w.Finalize()
	return err
}

// PacketizeVideo turns the video track into an RTP packet stream.
func PacketizeVideo(g *Generated) []*rtp.Packet {
	var pkts []*rtp.Packet
	seq := seqBase
	for fi, frame := range g.VideoData {
		chunks := (len(frame) + rtpDataPerPkt - 1) / rtpDataPerPkt
		for c := 0; c < chunks; c++ {
			end := (c + 1) * rtpDataPerPkt
			if end > len(frame) {
				end = len(frame)
			}
			pl := make([]byte, 5+end-c*rtpDataPerPkt)
			binary.BigEndian.PutUint32(pl[0:4], uint32(fi))
			if fi%25 == 0 {
				pl[4] = 0x01 // keyframe flag
			}
			copy(pl[5:], frame[c*rtpDataPerPkt:end])
			pkts = append(pkts, &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					SequenceNumber: seq,
					Timestamp:      uint32(int64(fi) * FrameDuration),
					Marker:         c == chunks-1,
					SSRC:           0x16601,
				},
				Payload: pl,
			})
			seq++
		}
	}
	return pkts
}

// WriteRTPCap stores packets as length-prefixed marshaled records.
func WriteRTPCap(path string, pkts []*rtp.Packet) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var lenBuf [4]byte
	for _, p := range pkts {
		raw, err := p.Marshal()
		if err != nil {
			return err
		}
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		if _, err := f.Write(lenBuf[:]); err != nil {
			return err
		}
		if _, err := f.Write(raw); err != nil {
			return err
		}
	}
	return nil
}

// ReadRTPCap reads a capture file, returning packets and the file offset of
// each record (start of its length prefix).
func ReadRTPCap(path string) ([]*rtp.Packet, []int64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var pkts []*rtp.Packet
	var offsets []int64
	pos := int64(0)
	for pos+4 <= int64(len(raw)) {
		n := int64(binary.BigEndian.Uint32(raw[pos : pos+4]))
		if pos+4+n > int64(len(raw)) {
			return pkts, offsets, fmt.Errorf("rtpcap: truncated record at offset %d", pos)
		}
		p := &rtp.Packet{}
		if err := p.Unmarshal(raw[pos+4 : pos+4+n]); err != nil {
			return pkts, offsets, err
		}
		pkts = append(pkts, p)
		offsets = append(offsets, pos)
		pos += 4 + n
	}
	return pkts, offsets, nil
}
