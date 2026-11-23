// Package fixture generates self-contained CMAF/fMP4 and RTP sample data
// for tests and the demo. No external files are referenced; every byte is
// produced deterministically from a seed so runs are reproducible.
package fixture

import (
	"math/rand"

	"mediatool/internal/mp4"
	clockrtp "mediatool/internal/rtp"

	pionrtp "github.com/pion/rtp"
)

// TrackSpec configures one generated track.
type TrackSpec struct {
	TrackID   uint32
	Timescale uint32
	MediaType string
	CodecKey  string
	// SampleDuration is the per-sample duration in track units; variation
	// is applied by the generator when VariableRate is set.
	SampleDuration uint32
	VariableRate   bool
}

// Fragment is one generated chunk plus the metadata the pipeline needs.
type Fragment struct {
	Seq       int
	TrackID   uint32
	DTS       uint64
	Bytes     []byte
	Durations []uint32
	Sizes     []uint32
	Payload   []byte
}

// Group bundles a full deterministic dataset.
type Group struct {
	Seed          int64
	Init          []byte
	Tracks        []mp4.TrackInfo
	VideoFrags    []Fragment
	AudioFrags    []Fragment
	// Corrupted holds byte slices that must be rejected by the parser.
	Corrupted     []CorruptCase
	RTPVideo      []*pionrtp.Packet
	RTPEvents     []clockrtp.Observation
	// LateAt marks the input order at which a fragment is delivered late.
	LateAt        int
}

// CorruptCase pairs a malformed byte slice with the expected error class.
type CorruptCase struct {
	Name  string
	Bytes []byte
	Class string
}

// VideoTrack and AudioTrack describe the canonical demo configuration.
func VideoTrack() TrackSpec {
	return TrackSpec{TrackID: 1, Timescale: 90000, MediaType: "video",
		CodecKey: "avc1", SampleDuration: 3003, VariableRate: true}
}

// AudioTrack mirrors VideoTrack for the audio stream.
func AudioTrack() TrackSpec {
	return TrackSpec{TrackID: 2, Timescale: 48000, MediaType: "audio",
		CodecKey: "mp4a", SampleDuration: 1024}
}

// Generate builds a group with n fragments per track. lateAt is the
// 0-based input order at which one video fragment is deliberately moved
// behind the watermark; pass -1 to disable the late-delivery scenario.
func Generate(seed int64, n int, lateAt int) *Group {
	if n < 1 {
		n = 1
	}
	rng := rand.New(rand.NewSource(seed))
	video := VideoTrack()
	audio := AudioTrack()
	tracks := []mp4.TrackInfo{
		{TrackID: video.TrackID, Timescale: video.Timescale,
			MediaType: video.MediaType, CodecKey: video.CodecKey},
		{TrackID: audio.TrackID, Timescale: audio.Timescale,
			MediaType: audio.MediaType, CodecKey: audio.CodecKey},
	}
	g := &Group{
		Seed:   seed,
		Init:   mp4.BuildInit(tracks),
		Tracks: tracks,
		LateAt: lateAt,
	}
	g.VideoFrags = genTrack(rng, video, n)
	g.AudioFrags = genTrack(rng, audio, n)
	g.Corrupted = genCorrupt(video)
	g.RTPVideo, g.RTPEvents = genRTP(rng, n)
	return g
}

// genTrack emits n ordered fragments with monotonically increasing DTS.
// Video durations vary around the nominal value to exercise VFR handling;
// audio stays fixed-frame as real AAC does.
func genTrack(rng *rand.Rand, spec TrackSpec, n int) []Fragment {
	out := make([]Fragment, 0, n)
	dts := uint64(0)
	for i := 0; i < n; i++ {
		sampleCount := 2 + rng.Intn(3) // 2..4 samples per fragment
		durations := make([]uint32, sampleCount)
		sizes := make([]uint32, sampleCount)
		var payload []byte
		for s := 0; s < sampleCount; s++ {
			d := spec.SampleDuration
			if spec.VariableRate {
				// +/- 10% variation, never zero.
				delta := int(float64(d) * (rng.Float64()*0.2 - 0.1))
				if d2 := int(d) + delta; d2 > 0 {
					d = uint32(d2)
				}
			}
			durations[s] = d
			size := 64 + rng.Intn(192) // 64..255 compressed bytes
			sizes[s] = uint32(size)
			buf := make([]byte, size)
			rng.Read(buf) // deterministic via seeded rng
			payload = append(payload, buf...)
		}
		spec2 := mp4.FragmentSpec{
			Seq:             i,
			TrackID:         spec.TrackID,
			BaseMediaDecode: dts,
			SampleDurations: durations,
			SampleSizes:     sizes,
			SamplePayload:   payload,
		}
		out = append(out, Fragment{
			Seq: i, TrackID: spec.TrackID, DTS: dts,
			Bytes: mp4.BuildFragment(spec2),
			Durations: durations, Sizes: sizes, Payload: payload,
		})
		var total uint64
		for _, d := range durations {
			total += uint64(d)
		}
		dts += total
	}
	return out
}

// genCorrupt builds parser-rejection fixtures.
func genCorrupt(spec TrackSpec) []CorruptCase {
	return []CorruptCase{
		{
			Name:  "truncated_header",
			Bytes: []byte{0x00, 0x00, 0x00},
			Class: "box_truncated",
		},
		{
			Name: "illegal_length",
			Bytes: append([]byte{0x00, 0x00, 0x00, 0x05},
				[]byte("moof")...),
			Class: "box_length",
		},
		{
			Name: "truncated_moof_payload",
			Bytes: append(
				mp4.BuildBox("moof", mp4.BuildMfhd(1))[:12],
				0xAA, 0xBB),
			Class: "box_truncated",
		},
	}
}

// genRTP builds a 90 kHz RTP stream containing one legal 32-bit wrap and
// paired wall-clock observations for drift estimation.
func genRTP(rng *rand.Rand, n int) ([]*pionrtp.Packet, []clockrtp.Observation) {
	pkts := make([]*pionrtp.Packet, n)
	events := make([]clockrtp.Observation, n)
	ts := uint32(0xFFFFFFF0) // close to the 32-bit boundary
	for i := 0; i < n; i++ {
		pkts[i] = &pionrtp.Packet{
			Header: pionrtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      ts,
				SSRC:           1,
			},
			Payload: []byte{byte(i)},
		}
		events[i] = clockrtp.Observation{
			WallMS:    int64(i) * 100,
			MediaTime: uint64(ts),
		}
		ts += 9000 // 100 ms of a 90 kHz clock; wraps naturally
	}
	return pkts, events
}

// ReorderLate moves fragment at lateAt to the end so it arrives behind the
// watermark. It returns a new slice without mutating the original.
func ReorderLate(in []Fragment, lateAt int) []Fragment {
	if lateAt < 0 || lateAt >= len(in) {
		return in
	}
	out := make([]Fragment, 0, len(in))
	out = append(out, in[:lateAt]...)
	out = append(out, in[lateAt+1:]...)
	out = append(out, in[lateAt])
	return out
}

// RollbackTfdt rewrites the tfdt base media decode time of a valid
// fragment backwards, simulating a device restart. The returned bytes
// remain structurally valid; only the timestamp value changes.
func RollbackTfdt(f Fragment, newBase uint64) []byte {
	spec := mp4.FragmentSpec{
		Seq:             f.Seq,
		TrackID:         f.TrackID,
		BaseMediaDecode: newBase,
		SampleDurations: f.Durations,
		SampleSizes:     f.Sizes,
		SamplePayload:   f.Payload,
	}
	return mp4.BuildFragment(spec)
}
