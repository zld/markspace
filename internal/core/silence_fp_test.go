package core_test

import (
	"testing"

	"mediad/internal/core"
	"mediad/internal/model"
)

func audioFrame(pts model.PlayTime, energy byte, truncated bool) model.Frame {
	// 128 is the unsigned 8-bit PCM zero point; energy is the deviation.
	return model.Frame{
		Kind: model.TrackAudio, PTS: pts, Dur: 1_000_000_000,
		Payload: []byte{128 - energy, 128 + energy, 128}, Truncated: truncated,
	}
}

func TestSilenceIntervals(t *testing.T) {
	opts := core.SilenceOptions{EnergyThreshold: 5, MinDuration: 0}
	frames := []model.Frame{
		audioFrame(0, 0, false),       // silent
		audioFrame(1_000_000_000, 0, false),
		audioFrame(2_000_000_000, 100, false), // loud
		audioFrame(3_000_000_000, 1, false),   // silent
	}
	rep := core.DetectSilence(frames, model.TrackAudio, opts)
	if rep.Incomplete {
		t.Fatalf("complete audio must not be incomplete: %s", rep.Reason)
	}
	if len(rep.Intervals) != 2 {
		t.Fatalf("intervals = %d: %+v", len(rep.Intervals), rep.Intervals)
	}
	if rep.Intervals[0].Start != 0 || rep.Intervals[0].Frames != 2 {
		t.Fatalf("first interval wrong: %+v", rep.Intervals[0])
	}
	if rep.Intervals[1].Start != 3_000_000_000 {
		t.Fatalf("second interval start = %d", rep.Intervals[1].Start)
	}
}

func TestSilenceMissingAudio(t *testing.T) {
	rep := core.DetectSilence(nil, model.TrackVideo,
		core.SilenceOptions{EnergyThreshold: 5})
	if !rep.Incomplete || rep.Reason == "" {
		t.Fatal("missing audio must be flagged incomplete with a reason")
	}
}

func TestSilenceTruncatedFrame(t *testing.T) {
	frames := []model.Frame{
		audioFrame(0, 0, false),
		audioFrame(1_000_000_000, 0, true), // truncated: cannot classify
	}
	rep := core.DetectSilence(frames, model.TrackAudio,
		core.SilenceOptions{EnergyThreshold: 5})
	if !rep.Incomplete {
		t.Fatal("truncated frame must make the report incomplete")
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	mk := func(id string, payloads ...string) core.Chunk {
		c := core.Chunk{ID: id, Representation: "r", Start: 0}
		for _, p := range payloads {
			c.Frames = append(c.Frames, model.Frame{Payload: []byte(p)})
		}
		return c
	}
	a := mk("r-a", "one", "two")
	b := mk("r-b", "one", "two")
	diff := mk("r-c", "one", "two!")
	ha, hb, hd := core.HashChunk(a), core.HashChunk(b), core.HashChunk(diff)
	if ha.Hash != hb.Hash {
		t.Fatal("identical content must hash equal")
	}
	if ha.Hash == hd.Hash {
		t.Fatal("different content must not collide")
	}
	if ha.PayloadBytes != 6 {
		t.Fatalf("payload bytes = %d", ha.PayloadBytes)
	}
}

func TestDedupeCrossRepresentation(t *testing.T) {
	mk := func(rep, id string, start model.PlayTime, payloads ...string) core.Chunk {
		c := core.Chunk{ID: id, Representation: rep, Start: start, End: start + 1}
		for _, p := range payloads {
			c.Frames = append(c.Frames, model.Frame{Payload: []byte(p)})
		}
		return c
	}
	chunks := map[string][]core.Chunk{
		"hi": {mk("hi", "hi-c0000", 0, "same"), mk("hi", "hi-c0001", 5, "only-hi")},
		"lo": {mk("lo", "lo-c0000", 0, "same"), mk("lo", "lo-c0001", 7, "only-lo")},
	}
	rep := core.DedupChunks(chunks, map[string]bool{})
	if rep.DuplicateCount != 1 {
		t.Fatalf("deduplicated = %d", rep.DuplicateCount)
	}
	var dup *core.DedupeRecord
	kept := map[string]int{}
	for i := range rep.Records {
		r := &rep.Records[i]
		if !r.Kept {
			dup = r
		}
		if r.Kept {
			kept[r.PrimaryRep]++
		}
	}
	if kept["hi"] != 2 || kept["lo"] != 1 {
		t.Fatalf("primary selection not deterministic: %+v", kept)
	}
	// The duplicate pointer preserves the low-representation source but
	// points at the high-representation primary.
	if dup.PrimaryRep != "hi" || dup.PrimaryChunkID != "hi-c0000" {
		t.Fatalf("duplicate points at wrong primary: %+v", dup)
	}
}

func TestDedupeBlockedOnMisalignment(t *testing.T) {
	mk := func(rep, id string, payloads ...string) core.Chunk {
		c := core.Chunk{ID: id, Representation: rep, Start: 0, End: 1}
		for _, p := range payloads {
			c.Frames = append(c.Frames, model.Frame{Payload: []byte(p)})
		}
		return c
	}
	chunks := map[string][]core.Chunk{
		"hi": {mk("hi", "hi-c0000", "x")},
		"lo": {mk("lo", "lo-c0000", "x")},
	}
	rep := core.DedupChunks(chunks, map[string]bool{"lo": true})
	if rep.DuplicateCount != 0 {
		t.Fatal("misaligned reps must never merge")
	}
	blocked := 0
	for _, r := range rep.Records {
		if r.BlockedMerge {
			blocked++
		}
	}
	if blocked == 0 {
		t.Fatal("expected blocked-merge records")
	}
}
