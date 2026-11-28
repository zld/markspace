// Package fixture generates every sample the acceptance run needs in-process:
// checksummed frame records, MP4 byte streams, disordered RTP packets and
// wrap-around tick series. No external media files are required.
package fixture

import (
	"bytes"

	"medcheck/internal/ingest"
	"medcheck/internal/model"
)

// FrameSpec is one generated frame before checksum/offset assignment.
type FrameSpec struct {
	Track   uint32
	Source  uint32
	Seq     uint32
	Ticks   int64
	Type    model.FrameType
	Payload []byte
	Corrupt bool
}

// FrameSet is a named collection of frames with real CRCs and byte offsets.
type FrameSet struct {
	Name    string
	Frames  []*model.Frame
	Raw     []byte
	LateSeq []uint32 // sequences that arrive late, after the base water mark
	Base    model.Rational
}

func baseScale() model.Rational { return model.Rational{Num: 1_000_000, Den: 1} }

// encode runs specs through the real ingest writer and reader so every frame
// carries a genuine CRC32 result and original byte offset.
func encode(name string, specs []FrameSpec, lateSeq []uint32) *FrameSet {
	var raw bytes.Buffer
	ins := make([]ingest.EncodeInput, 0, len(specs))
	for _, s := range specs {
		ins = append(ins, ingest.EncodeInput{
			Track:      s.Track,
			Source:     s.Source,
			Seq:        s.Seq,
			EventTicks: s.Ticks,
			Base:       baseScale(),
			FrameType:  s.Type,
			Payload:    s.Payload,
			Corrupt:    s.Corrupt,
		})
	}
	for _, in := range ins {
		rec, err := ingest.EncodeRecord(in)
		if err != nil {
			panic(err)
		}
		raw.Write(rec)
	}
	frames, err := ingest.NewReader(bytes.NewReader(raw.Bytes()), name).ReadAll()
	if err != nil {
		panic(err)
	}
	return &FrameSet{Name: name, Frames: frames, Raw: raw.Bytes(), LateSeq: lateSeq, Base: baseScale()}
}

func keyType(i int) model.FrameType {
	if i%3 == 0 {
		return model.TypeKeyFrame
	}
	return model.TypeDelta
}

func payload(b byte) []byte { return []byte{'p', 'a', 'y', b} }

// Normal builds a clean, monotonically timed single-source frame set.
func Normal() *FrameSet {
	specs := make([]FrameSpec, 8)
	for i := 0; i < 8; i++ {
		specs[i] = FrameSpec{
			Track: 1, Source: 1, Seq: uint32(i),
			Ticks: int64(i) * 10, Type: keyType(i), Payload: payload(byte(i)),
		}
	}
	return encode("normal", specs, nil)
}

// CRCCorrupt places two bad frames separated by valid frames so they form at
// least two distinct damage segments.
func CRCCorrupt() *FrameSet {
	specs := make([]FrameSpec, 8)
	for i := 0; i < 8; i++ {
		specs[i] = FrameSpec{
			Track: 1, Source: 1, Seq: uint32(i),
			Ticks: int64(i) * 10, Type: keyType(i), Payload: payload(byte(i)),
			Corrupt: i == 2 || i == 5,
		}
	}
	return encode("crc", specs, nil)
}

// SourceSwitch emits two sources on the same track without interleaving them.
func SourceSwitch() *FrameSet {
	var specs []FrameSpec
	for i := 0; i < 4; i++ {
		specs = append(specs, FrameSpec{
			Track: 1, Source: 1, Seq: uint32(i),
			Ticks: int64(i) * 10, Type: keyType(i), Payload: payload(byte(i)),
		})
	}
	for i := 0; i < 4; i++ {
		specs = append(specs, FrameSpec{
			Track: 1, Source: 2, Seq: uint32(i),
			Ticks: 40 + int64(i)*10, Type: keyType(i), Payload: payload(byte(i + 10)),
		})
	}
	return encode("source_switch", specs, nil)
}

// LateFrames returns a base set plus two in-window late frames. The late frames
// can be replayed in either order without changing the deterministic result.
func LateFrames() (base *FrameSet, late []*model.Frame) {
	specs := make([]FrameSpec, 6)
	for i := 0; i < 6; i++ { // base event times 0..50
		specs[i] = FrameSpec{
			Track: 1, Source: 1, Seq: uint32(i),
			Ticks: int64(i) * 10, Type: keyType(i), Payload: payload(byte(i)),
		}
	}
	base = encode("late_base", specs, nil)
	lateSpecs := []FrameSpec{
		{Track: 1, Source: 1, Seq: 100, Ticks: 48, Type: model.TypeDelta, Payload: payload(100)},
		{Track: 1, Source: 1, Seq: 101, Ticks: 49, Type: model.TypeDelta, Payload: payload(101)},
	}
	lateSet := encode("late_frames", lateSpecs, []uint32{100, 101})
	return base, lateSet.Frames
}

// AllFrameSets returns every frame-based sample for the acceptance run.
func AllFrameSets() []*FrameSet {
	n := Normal()
	c := CRCCorrupt()
	sw := SourceSwitch()
	return []*FrameSet{n, c, sw}
}
