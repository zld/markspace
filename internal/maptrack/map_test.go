package maptrack

import (
	"bytes"
	"testing"

	"mediad/internal/fault"
)

func stereoLayout() Layout {
	return Layout{
		Channels:      2,
		SampleRate:    48000,
		BitsPerSample: 16,
		ChannelOrder:  []string{"L", "R"},
	}
}

func TestValidateRejectsBadLayouts(t *testing.T) {
	good := stereoLayout()
	if err := Validate(good); err != nil {
		t.Fatalf("valid layout rejected: %v", err)
	}
	bad := []Layout{
		{Channels: 0, SampleRate: 48000, BitsPerSample: 16, ChannelOrder: nil},
		{Channels: 9, SampleRate: 48000, BitsPerSample: 16, ChannelOrder: make([]string, 9)},
		{Channels: 2, SampleRate: 48000, BitsPerSample: 8, ChannelOrder: []string{"L", "R"}},
		{Channels: 2, SampleRate: 0, BitsPerSample: 16, ChannelOrder: []string{"L", "R"}},
		{Channels: 2, SampleRate: 48000, BitsPerSample: 16, ChannelOrder: []string{"L"}},
		{Channels: 2, SampleRate: 48000, BitsPerSample: 16, ChannelOrder: []string{"L", "L"}},
	}
	for i, l := range bad {
		if err := Validate(l); err == nil {
			t.Fatalf("case %d: invalid layout accepted", i)
		}
	}
}

func TestBuildRejectsAmbiguousMappings(t *testing.T) {
	src := stereoLayout()
	cases := []struct {
		name string
		maps []Mapping
	}{
		{"empty", nil},
		{"unknown channel", []Mapping{{TrackID: 0, SourceChannel: "C"}}},
		{"duplicate track", []Mapping{
			{TrackID: 0, SourceChannel: "L"},
			{TrackID: 0, SourceChannel: "R"},
		}},
		{"duplicate source", []Mapping{
			{TrackID: 0, SourceChannel: "L"},
			{TrackID: 1, SourceChannel: "L"},
		}},
		{"unmapped channel", []Mapping{{TrackID: 0, SourceChannel: "L"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Build(src, c.maps); err == nil {
				t.Fatal("ambiguous mapping accepted")
			} else if fe, ok := fault.As(err); !ok || fe.Category != fault.CatMapping &&
				fe.Category != fault.CatHeader {
				t.Fatalf("wrong fault category: %v", err)
			}
		})
	}
}

func TestDefaultMonoPlanAndSplit(t *testing.T) {
	src := Layout{
		Channels:      3,
		SampleRate:    44100,
		BitsPerSample: 24,
		ChannelOrder:  []string{"L", "R", "C"},
	}
	plan, err := DefaultMonoPlan(src)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TrackCount() != 3 {
		t.Fatalf("track count = %d", plan.TrackCount())
	}
	// One 24-bit/3-channel frame: 9 bytes.
	frame := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	samples, err := plan.SplitFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}}
	for i := range want {
		if !bytes.Equal(samples[i], want[i]) {
			t.Fatalf("track %d samples = %v want %v", i, samples[i], want[i])
		}
	}
	// Re-interleaving in source channel order must reproduce the input exactly.
	rebuilt := make([]byte, 0, len(frame))
	for srcCh := 0; srcCh < int(src.Channels); srcCh++ {
		track := -1
		for tr, idx := range plan.indices {
			if idx == srcCh {
				track = tr
				break
			}
		}
		rebuilt = append(rebuilt, samples[track]...)
	}
	if !bytes.Equal(rebuilt, frame) {
		t.Fatalf("rebuilt frame %v != original %v", rebuilt, frame)
	}
}

func TestCustomTrackOrder(t *testing.T) {
	src := stereoLayout()
	// Explicitly place R on track 0 and L on track 1.
	plan, err := Build(src, []Mapping{
		{TrackID: 0, SourceChannel: "R"},
		{TrackID: 1, SourceChannel: "L"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.SourceIndex(0) != 1 || plan.SourceIndex(1) != 0 {
		t.Fatalf("source indices wrong: %d %d", plan.SourceIndex(0), plan.SourceIndex(1))
	}
	frame := []byte{0xAA, 0xBB, 0xCC, 0xDD}
	samples, err := plan.SplitFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(samples[0], []byte{0xCC, 0xDD}) ||
		!bytes.Equal(samples[1], []byte{0xAA, 0xBB}) {
		t.Fatalf("custom ordering failed: %v", samples)
	}
}

func TestSplitRejectsWrongFrameSize(t *testing.T) {
	plan, err := DefaultMonoPlan(stereoLayout())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plan.SplitFrame([]byte{1, 2, 3}); err == nil {
		t.Fatal("partial frame must be rejected")
	} else if fe, ok := fault.As(err); !ok || fe.Category != fault.CatAlignment {
		t.Fatalf("wrong fault: %v", err)
	}
}
