package timeline

import (
	"testing"

	"subx/internal/cue"
)

func TestParseFPS(t *testing.T) {
	for s, want := range map[string]FPS{"24": {24, 1}, "24000/1001": {24000, 1001}, "30": {30, 1}} {
		got, err := ParseFPS(s)
		if err != nil || got != want {
			t.Fatalf("ParseFPS(%q) = %v, %v", s, got, err)
		}
	}
	for _, bad := range []string{"", "0", "-24", "24/0", "23.976", "a/b", "1/2/3", "1001"} {
		if _, err := ParseFPS(bad); err == nil {
			t.Fatalf("ParseFPS(%q) accepted", bad)
		}
	}
}

func TestFrameMapping(t *testing.T) {
	fps, _ := ParseFPS("24000/1001") // frame duration ~41.708ms
	if got := fps.StartFrame(1000); got != 23 {
		t.Fatalf("StartFrame(1000) = %d", got)
	}
	if got := fps.EndFrame(3000); got != 72 {
		t.Fatalf("EndFrame(3000) = %d", got)
	}
	// serialized frame times must map back to the same frames
	for frame := int64(0); frame < 500; frame += 7 {
		if got := fps.StartFrame(fps.FrameStartMs(frame)); got != frame {
			t.Fatalf("start frame %d round trip = %d", frame, got)
		}
		if got := fps.EndFrame(fps.FrameEndMs(frame)); got != frame {
			t.Fatalf("end frame %d round trip = %d", frame, got)
		}
	}
	// boundary delta never exceeds one frame duration (~41.71ms)
	var maxDelta int64
	for ms := int64(0); ms < 60000; ms += 13 {
		d := fps.FrameStartMs(fps.StartFrame(ms)) - ms
		if d < 0 {
			d = -d
		}
		if d > maxDelta {
			maxDelta = d
		}
	}
	if maxDelta > 42 {
		t.Fatalf("max start delta %dms exceeds one frame", maxDelta)
	}
}

func TestMapKeepsSubFrameCues(t *testing.T) {
	fps, _ := ParseFPS("30") // 33.3ms frames
	mapped := Map([]cue.Cue{{StartMs: 100, EndMs: 101}}, fps) // 1ms cue
	if mapped[0].EndFrame-mapped[0].StartFrame != 1 {
		t.Fatalf("sub-frame cue got %d frames", mapped[0].EndFrame-mapped[0].StartFrame)
	}
}

func fc(id string, sf, ef int64) FrameCue {
	return FrameCue{Cue: cue.Cue{ID: id, Lines: []string{"x"}}, StartFrame: sf, EndFrame: ef}
}

func TestResolveTrim(t *testing.T) {
	in := []FrameCue{fc("a", 0, 10), fc("b", 5, 8), fc("c", 8, 12), fc("d", 20, 25)}
	kept, dropped, err := Resolve(in, Trim)
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 3 || len(dropped) != 1 || dropped[0].ID != "b" {
		t.Fatalf("kept=%v dropped=%v", kept, dropped)
	}
	if kept[1].ID != "c" || kept[1].StartFrame != 10 || !kept[1].Trimmed {
		t.Fatalf("cue c not trimmed to 10: %+v", kept[1])
	}
	if kept[0].Trimmed || kept[2].Trimmed {
		t.Fatal("unrelated cues marked trimmed")
	}
}

func TestResolveKeepAndError(t *testing.T) {
	in := []FrameCue{fc("a", 0, 10), fc("b", 5, 8)}
	kept, dropped, err := Resolve(in, Keep)
	if err != nil || len(kept) != 2 || len(dropped) != 0 {
		t.Fatalf("keep: kept=%d dropped=%d err=%v", len(kept), len(dropped), err)
	}
	if _, _, err = Resolve(in, Reject); err == nil {
		t.Fatal("error policy accepted overlap")
	} else if _, ok := err.(*OverlapError); !ok {
		t.Fatalf("want *OverlapError, got %T", err)
	}
}

func TestResolveStableOrder(t *testing.T) {
	in := []FrameCue{fc("b", 5, 8), fc("a", 0, 10), fc("c", 5, 6)}
	kept, _, err := Resolve(in, Keep)
	if err != nil {
		t.Fatal(err)
	}
	if kept[0].ID != "a" || kept[1].ID != "b" || kept[2].ID != "c" {
		t.Fatalf("order: %q %q %q", kept[0].ID, kept[1].ID, kept[2].ID)
	}
}
