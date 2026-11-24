// Package fixture generates the synthetic multi-format inputs used by
// tests and by `mediad verify --self-generate`. No external media is ever
// read; every byte derives from the seed.
package fixture

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediad/internal/media"
	"mediad/internal/pipeline"
)

// Options controls fixture generation.
type Options struct {
	Seed     int64
	Segments int
	Corrupt  string // "", "truncated-box", "invalid-edit-list"
	Dir      string
}

// Layout describes the generated input set.
type Layout struct {
	ManifestPath     string
	Inputs           []string
	ExpectedDriftPPM float64
	VideoSamples     int
}

// inputManifest is the multi-format delivery list read by the pipeline.
type inputManifest struct {
	Version string       `json:"version"`
	Inputs  []inputEntry `json:"inputs"`
}

type inputEntry struct {
	Path      string `json:"path"`
	Container string `json:"container"`
}

const (
	videoTimescale = 90000
	audioTimescale = 48000
	rtpClock       = 90000
	samplesPerSeg  = 25
	keyframeEvery  = 30
)

// Generate writes the fixture set and returns its layout.
func Generate(opts Options) (*Layout, error) {
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewSource(opts.Seed))
	n := opts.Segments * samplesPerSeg

	// --- video track: VFR deltas, sparse keyframes, stss present ---
	video := pipeline.WriteTrack{
		TrackID: 1, Type: media.TrackVideo, Language: "und",
		CodecParams: stsd("avc1", 16),
		Timescale:   videoTimescale,
	}
	vfrPattern := []uint32{1500, 3000, 4500} // 16.6/33.3/50ms @90kHz
	for i := 0; i < n; i++ {
		d := vfrPattern[rng.Intn(len(vfrPattern))]
		video.Deltas = append(video.Deltas, d)
		key := i%keyframeEvery == 0
		if key {
			video.Keyframes = append(video.Keyframes, i)
		}
		video.Samples = append(video.Samples, payload(rng, key, 200+rng.Intn(600)))
	}
	var videoTicks uint64
	for _, d := range video.Deltas {
		videoTicks += uint64(d)
	}

	// --- audio track 1: eng, edit list with non-zero media start ---
	audio1 := pipeline.WriteTrack{
		TrackID: 2, Type: media.TrackAudio, Language: "eng",
		CodecParams: stsd("mp4a", 8),
		Timescale:   audioTimescale,
	}
	audioNSamples := int(videoTicks * audioTimescale / videoTimescale / 1024)
	for i := 0; i < audioNSamples; i++ {
		audio1.Deltas = append(audio1.Deltas, 1024)
		audio1.Samples = append(audio1.Samples, payload(rng, false, 96))
	}
	startNs := media.TicksToNanos(audioTimescale, audioTimescale) // 1s
	totalNs := media.TicksToNanos(int64(audioNSamples)*1024, audioTimescale)
	audio1.Edits = []media.Edit{{MediaTime: startNs, Duration: totalNs - startNs, Rate: 1}}

	// --- audio track 2: duplicate language, missing codec parameters ---
	audio2 := pipeline.WriteTrack{
		TrackID: 3, Type: media.TrackAudio, Language: "eng",
		CodecParams: stsdEmpty(), // entry count 0: parameters missing
		Timescale:   audioTimescale,
	}
	for i := 0; i < audioNSamples/2; i++ {
		audio2.Deltas = append(audio2.Deltas, 1024)
		audio2.Samples = append(audio2.Samples, payload(rng, false, 64))
	}

	if opts.Corrupt == "invalid-edit-list" {
		// media_time far beyond the media duration: unresolvable.
		video.Edits = []media.Edit{{
			MediaTime: media.TicksToNanos(int64(videoTicks)*10, videoTimescale),
			Duration:  media.TicksToNanos(int64(videoTicks), videoTimescale),
			Rate:      1,
		}}
	}

	var mp4buf bytes.Buffer
	if err := pipeline.WriteMP4(&mp4buf, []pipeline.WriteTrack{video, audio1, audio2}); err != nil {
		return nil, err
	}
	mp4Bytes := mp4buf.Bytes()
	if opts.Corrupt == "truncated-box" {
		mp4Bytes = mp4Bytes[:len(mp4Bytes)*9/10] // cut the mdat tail
	}
	mp4Path := filepath.Join(opts.Dir, "cam_a.mp4")
	if err := os.WriteFile(mp4Path, mp4Bytes, 0o644); err != nil {
		return nil, err
	}

	// --- RTP capture: wrap start, drift jump, one loss, reorder at wrap ---
	rtpPath := filepath.Join(opts.Dir, "cam_b.rtpcap")
	if err := writeRTPCap(rtpPath, rng, videoTicks); err != nil {
		return nil, err
	}

	man := inputManifest{Version: "v1", Inputs: []inputEntry{
		{Path: "cam_a.mp4", Container: "mp4"},
		{Path: "cam_b.rtpcap", Container: "rtpcap"},
	}}
	manBytes, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return nil, err
	}
	manPath := filepath.Join(opts.Dir, "manifest.json")
	if err := os.WriteFile(manPath, manBytes, 0o644); err != nil {
		return nil, err
	}
	return &Layout{
		ManifestPath:     manPath,
		Inputs:           []string{mp4Path, rtpPath},
		ExpectedDriftPPM: 200,
		VideoSamples:     n,
	}, nil
}

// stsd builds a raw stsd payload: version/flags, one entry of the given
// fourcc with paramLen opaque parameter bytes.
func stsd(fourcc string, paramLen int) []byte {
	entryLen := 8 + paramLen
	out := make([]byte, 0, 8+entryLen)
	out = append(out, 0, 0, 0, 0) // version/flags
	out = append(out, 0, 0, 0, 1) // entry count
	var sz [4]byte
	binary.BigEndian.PutUint32(sz[:], uint32(entryLen))
	out = append(out, sz[:]...)
	out = append(out, fourcc...)
	for i := 0; i < paramLen; i++ {
		out = append(out, byte(0xA0+i))
	}
	return out
}

// stsdEmpty is an stsd payload declaring zero entries: codec parameters
// are missing and must be carried as raw bytes only.
func stsdEmpty() []byte {
	return []byte{0, 0, 0, 0, 0, 0, 0, 0}
}

// payload builds one compressed sample stand-in: byte 0 bit 0 = keyframe
// flag (the scan-recovery convention), the rest deterministic noise.
func payload(rng *rand.Rand, keyframe bool, size int) []byte {
	p := make([]byte, size)
	rng.Read(p)
	if keyframe {
		p[0] = 1
	} else {
		p[0] = 0
	}
	return p
}

// writeRTPCap writes the RTP capture: timestamps start just below the
// 32-bit wrap, drift jumps from 200ppm to 400ppm (plus a 50ms offset step)
// at 60%, one packet is lost mid-stream and two packets swap order right
// after the wrap.
func writeRTPCap(path string, rng *rand.Rand, videoTicks uint64) error {
	durNs := media.TicksToNanos(int64(videoTicks), videoTimescale)
	count := int(durNs / 20_000_000) // 20ms packets
	type rec struct {
		arrival int64
		pkt     []byte
	}
	var recs []rec
	baseTS := ^uint32(0) - 999 // start just before the 32-bit wrap
	const baseSeq = 5000
	dropAt := count * 2 / 5
	jumpAt := count * 3 / 5
	wrapIdx := -1
	for i := 0; i < count; i++ {
		if i == dropAt {
			continue // lost packet -> recoverable gap
		}
		arrival := int64(i) * 20_000_000
		ppm := 200.0
		extra := int64(0)
		if i >= jumpAt {
			ppm = 400
			extra = 4_500_000 // 50ms abrupt offset
		}
		ticks := int64(float64(i)*1800*(1+ppm/1e6)) + extra
		ts := baseTS + uint32(ticks)
		if wrapIdx < 0 && ts < baseTS {
			wrapIdx = i
		}
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: uint16(baseSeq + i),
				Timestamp:      ts,
				SSRC:           1001,
				PayloadType:    96,
			},
			Payload: payload(rng, i%50 == 0, 160),
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return err
		}
		recs = append(recs, rec{arrival: arrival, pkt: raw})
	}
	// Swap the two packets straddling the wrap: out-of-order delivery.
	if wrapIdx > 0 && wrapIdx < len(recs) {
		recs[wrapIdx-1], recs[wrapIdx] = recs[wrapIdx], recs[wrapIdx-1]
	}

	var buf bytes.Buffer
	buf.WriteString("RTPC")
	for _, r := range recs {
		var hdr [10]byte
		binary.BigEndian.PutUint64(hdr[0:8], uint64(r.arrival))
		binary.BigEndian.PutUint16(hdr[8:10], uint16(len(r.pkt)))
		buf.Write(hdr[:])
		buf.Write(r.pkt)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write rtpcap: %w", err)
	}
	return nil
}
