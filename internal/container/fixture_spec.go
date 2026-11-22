package container

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"archivist/internal/model"
)

// Corruption kinds supported by the fixture generator (spec function 6).
const (
	CorruptNone            = ""
	CorruptTruncatedBox    = "truncated-box"
	CorruptSampleTable     = "corrupt-sample-table"
	CorruptMissingKeyframe = "missing-keyframe-index"
	CorruptAudioOffset     = "audio-start-offset"
	CorruptKeyframeDrift   = "keyframe-drift"
)

// TrackSpec describes one generated track.
type TrackSpec struct {
	ID           uint32
	Kind         string // model.KindVideo / model.KindAudio
	Timescale    uint32
	SampleCount  uint32
	SampleDur    uint32 // constant per-sample duration in track units
	KeyEvery     uint32 // video: keyframe period in samples (0 = audio)
	Channels     uint16
	SampleRate   uint32
	Color        model.ColorMeta
	StartDelay   uint64 // audio: edit-list media-time offset (track units)
	BitrateLabel string // "low" / "high"
}

// FixtureSpec is the deterministic input to the generator.
type FixtureSpec struct {
	Name         string
	Seed         int64
	Brand        string // "isom" (MP4) or "qt  " (MOV)
	MovieTS      uint32
	Tracks       []TrackSpec
	Corruption   string
	DriftSamples uint32 // keyframe shift for the high-bitrate video track
}

// Fixture is a generated container plus generation metadata for tests.
type Fixture struct {
	Name                string
	Data                []byte
	Spec                FixtureSpec
	MdatOffset          uint64
	MdatPayload         []byte
	ExpectedSampleSizes map[uint32][]uint32
	Digest              string
}

// markerKeyframe is the last payload byte of every keyframe sample. It lets a
// missing stss index be rebuilt directly from bitstream content.
const markerKeyframe byte = 0xA6

type generator struct {
	spec        FixtureSpec
	out         Writer
	mdat        Writer
	mdatOffset  uint64
	mdatPayload []byte
	payloads    map[uint32][]byte // per-track concatenated sample bytes
	sizes       map[uint32][]uint32
}

func newGenerator(spec FixtureSpec) *generator {
	return &generator{
		spec:     spec,
		payloads: map[uint32][]byte{},
		sizes:    map[uint32][]uint32{},
	}
}

func trackDuration(ts TrackSpec) uint64 {
	return uint64(ts.SampleCount) * uint64(ts.SampleDur)
}

// sampleByte derives deterministic payload for one sample.
func sampleByte(seed int64, trackID, idx uint32) byte {
	h := sha256.Sum256([]byte(fmt.Sprintf("archivist:%d:%d:%d", seed, trackID, idx)))
	return h[idx%uint32(len(h))]
}

// boxBytes serializes a single leaf box without a Writer.
func boxBytes(t FourCC, payload []byte) []byte {
	var w Writer
	w.WriteBox(t, payload)
	return w.Bytes()
}

// keyframeSamples returns the 0-based keyframe sample numbers for a track.
func (g *generator) keyframeSamples(ts TrackSpec) []uint32 {
	if ts.KeyEvery == 0 {
		return nil
	}
	var keys []uint32
	start := uint32(0)
	if g.spec.Corruption == CorruptKeyframeDrift && ts.BitrateLabel == "high" {
		start = g.spec.DriftSamples // shifted GOP origin
	}
	for i := start; i < ts.SampleCount; i += ts.KeyEvery {
		keys = append(keys, i)
	}
	return keys
}

// buildPayloads fills per-track sample bytes and stamps keyframe markers.
func (g *generator) buildPayloads() {
	for _, ts := range g.spec.Tracks {
		size := uint32(64)
		if ts.Kind == model.KindAudio {
			size = 32
		}
		keySet := map[uint32]bool{}
		for _, k := range g.keyframeSamples(ts) {
			keySet[k] = true
		}
		buf := make([]byte, 0, int(ts.SampleCount)*int(size))
		for i := uint32(0); i < ts.SampleCount; i++ {
			chunk := make([]byte, size)
			base := sampleByte(g.spec.Seed, ts.ID, i)
			for j := range chunk {
				chunk[j] = base ^ byte(j) ^ byte(i)
			}
			if keySet[i] {
				chunk[len(chunk)-1] = markerKeyframe
			}
			buf = append(buf, chunk...)
			g.sizes[ts.ID] = append(g.sizes[ts.ID], size)
		}
		g.payloads[ts.ID] = buf
	}
}

// digest returns the sha256 of the serialized container.
func (g *generator) digest() string {
	sum := sha256.Sum256(g.out.Bytes())
	return hex.EncodeToString(sum[:])
}
