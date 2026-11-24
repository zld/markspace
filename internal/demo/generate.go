package demo

import (
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"mediad/internal/audio"
	"mediad/internal/ingest"
	"mediad/internal/mederr"
)

// GeneratedTrack is a deterministic source before format normalization.
type GeneratedTrack struct {
	ID     string
	Buffer *audio.Buffer
}

// GenerateTone creates a seeded, bounded harmonic signal with a short edge
// fade to avoid synthetic clicks. Different track indices use distinct
// frequencies and channel decorrelation.
func GenerateTone(seed int64, index, rate, channels, seconds int) *audio.Buffer {
	frames := rate * seconds
	layout := audio.Layout{Name: "custom", Channels: channels}
	if channels == 1 {
		layout = audio.LayoutMono
	} else if channels == 2 {
		layout = audio.LayoutStereo
	}
	b := audio.MustNew(rate, layout, frames)
	rng := rand.New(rand.NewSource(seed + int64(index)*100003))
	base := 180 + float64(index)*73 + rng.Float64()*40
	fade := rate / 100 // 10 ms
	for i := 0; i < frames; i++ {
		t := float64(i) / float64(rate)
		edge := 1.0
		if i < fade {
			edge = float64(i) / float64(fade)
		} else if i > frames-fade {
			edge = float64(frames-i) / float64(fade)
		}
		for c := 0; c < channels; c++ {
			phase := 1 + 0.003*float64(c+1)*math.Sin(2*math.Pi*5*t)
			v := 0.16 * math.Sin(2*math.Pi*base*phase*t)
			v += 0.07 * math.Sin(2*math.Pi*base*2.01*t+float64(c))
			v += 0.035 * (rng.Float64()*2 - 1)
			b.Planar[c][i] = v * edge
		}
	}
	return b
}

func writeEDLFixture(path string) (*ingest.EDL, error) {
	edl := &ingest.EDL{Segments: []ingest.Segment{
		{Track: "mp4:1", StartNanos: 0, DurationNanos: 4_000_000_000},
		{Track: "rtp:0", StartNanos: 3_500_000_000, DurationNanos: 4_000_000_000, TransitionInNanos: 500_000_000},
		{Track: "mp4:2", StartNanos: 7_000_000_000, DurationNanos: 4_000_000_000, TransitionInNanos: 500_000_000},
	}}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := ingest.WriteEDL(f, edl); err != nil {
		return nil, err
	}
	return edl, nil
}

func writeRTPFixture(path string, pcm []byte, rate int, channels int, wrapAt uint32, ppm float64) error {
	const samplesPerPacket = 960 // 20 ms at 48 kHz
	frameBytes := channels * 2
	packetBytes := samplesPerPacket * frameBytes
	if len(pcm)%packetBytes != 0 {
		return mederr.New(mederr.CatAudio, "rtp:0", -1, -1, false,
			"RTP PCM length %d not aligned to packet size %d", len(pcm), packetBytes)
	}
	f, err := os.Create(path)
	if err != nil {
		return mederr.New(mederr.CatIO, "rtp:0", -1, -1, false, "create RTP fixture: %v", err)
	}
	defer f.Close()
	packets := len(pcm) / packetBytes
	for i := 0; i < packets; i++ {
		arrival := int64(i) * 20_000_000
		remoteTicks := int64(math.Round(float64(arrival) * float64(rate) / 1e9 * (1 + ppm/1e6)))
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      wrapAt + uint32(remoteTicks),
				SSRC:           0x4d454431,
				Marker:         i == 0,
			},
			Payload: pcm[i*packetBytes : (i+1)*packetBytes],
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return mederr.New(mederr.CatParse, "rtp:0", -1, -1, false, "marshal RTP: %v", err)
		}
		hdr := make([]byte, 12)
		binary.BigEndian.PutUint32(hdr[0:4], uint32(len(raw)))
		binary.BigEndian.PutUint64(hdr[4:12], uint64(arrival))
		if _, err := f.Write(hdr); err != nil {
			return mederr.New(mederr.CatIO, "rtp:0", -1, -1, false, "write RTP header: %v", err)
		}
		if _, err := f.Write(raw); err != nil {
			return mederr.New(mederr.CatIO, "rtp:0", -1, -1, false, "write RTP packet: %v", err)
		}
	}
	return nil
}
