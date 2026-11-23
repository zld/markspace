package adapter

import (
	"bytes"
	"testing"
)

func mp4A() MP4Adapter { return MP4Adapter{} }

func TestMP4RoundTripAndUnknownEmsg(t *testing.T) {
	spec := MP4Spec{Timescale: 25, Delta: 1, Sizes: []uint32{16, 16, 16}, Keyframes: []uint32{1},
		Emsg: [][]byte{{1, 2, 3, 4}}}
	data, err := EncodeMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	res, err := mp4A().Parse(bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatal(err)
	}
	tr := res.Tracks[0]
	if len(tr.Events) != 3 || !tr.Events[0].Keyframe || tr.Events[1].Keyframe {
		t.Fatalf("keyframes/samples wrong: %+v", tr.Events)
	}
	if tr.Clock.Timescale != 25 || tr.Events[2].Tick != 2 {
		t.Fatalf("timescale/ticks wrong: ts=%d tick=%d", tr.Clock.Timescale, tr.Events[2].Tick)
	}
	if len(res.Unknown) != 1 || string(res.Unknown[0].MetaType) != "emsg" {
		t.Fatalf("emsg not preserved: %+v", res.Unknown)
	}
}

func TestMP4TruncationPropagates(t *testing.T) {
	data, err := EncodeMP4(MP4Spec{Timescale: 25, Delta: 1, Sizes: []uint32{16, 16}})
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{len(data) - 1, len(data) / 2, 20} {
		if _, err := mp4A().Parse(bytes.NewReader(data[:cut]), Options{}); err == nil {
			t.Fatalf("truncated mp4 at %d must fail", cut)
		}
	}
}

func TestMP4CorruptSampleMarked(t *testing.T) {
	data, err := EncodeMP4(MP4Spec{Timescale: 25, Delta: 1, Sizes: []uint32{16, 16}})
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte inside the second sample payload (after mdat header + first sample).
	idx := bytes.Index(data, []byte("MEDI"))
	second := idx + 16
	data[second] ^= 0xFF
	res, err := mp4A().Parse(bytes.NewReader(data), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Tracks[0].Events[1].Corrupt || res.Tracks[0].Events[0].Corrupt {
		t.Fatal("only the damaged sample must be marked corrupt")
	}
}

func TestDefaultRegistryServesAllAdapters(t *testing.T) {
	def := Default()
	for _, name := range []string{"metadata", "mp4", "rtp"} {
		if _, err := def.Get(name, Version{Major: 1, Minor: 0}); err != nil {
			t.Fatalf("default registry missing %s: %v", name, err)
		}
	}
}
