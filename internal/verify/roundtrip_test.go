package verify

import (
	"testing"

	"subx/internal/cue"
	"subx/internal/format"
	"subx/internal/timeline"
)

func TestCheckRoundTrip(t *testing.T) {
	fps, _ := timeline.ParseFPS("24000/1001")
	cues := []cue.Cue{
		{ID: "1", StartMs: 1000, EndMs: 3000, Lines: []string{"hello"}},
		{ID: "2", StartMs: 3500, EndMs: 5000, Lines: []string{"多行", "文本"}},
	}
	mapped := timeline.Map(cues, fps)
	kept, _, err := timeline.Resolve(mapped, timeline.Keep)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]cue.Cue, len(kept))
	for i, fc := range kept {
		out[i] = cue.Cue{ID: fc.ID, StartMs: fps.FrameStartMs(fc.StartFrame),
			EndMs: fps.FrameEndMs(fc.EndFrame), Lines: fc.Lines}
	}
	serialized := format.SerializeSRT(out)
	results, maxDelta, err := Check(serialized, format.SRT, kept, fps, "out.srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results", len(results))
	}
	if maxDelta > 42 {
		t.Fatalf("max delta %d exceeds one frame at 24000/1001", maxDelta)
	}
	// corrupt the output: count mismatch must be caught
	if _, _, err := Check(format.SerializeSRT(out[:1]), format.SRT, kept, fps, "out.srt"); err == nil {
		t.Fatal("count mismatch not detected")
	} else if _, ok := err.(*MismatchError); !ok {
		t.Fatalf("want *MismatchError, got %T", err)
	}
	// corrupt text
	bad := []cue.Cue{out[0], out[1]}
	bad[1].Lines = []string{"tampered", "文本"}
	if _, _, err := Check(format.SerializeSRT(bad), format.SRT, kept, fps, "out.srt"); err == nil {
		t.Fatal("text mismatch not detected")
	}
}
