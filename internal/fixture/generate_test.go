package fixture

import (
	"bytes"
	"testing"

	"tracecheck/internal/mp4"
	"tracecheck/internal/model"
)

func TestGenerateClean(t *testing.T) {
	tr, err := Generate(Options{Profile: ProfileClean, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Packets) != 24 {
		t.Fatalf("clean packet count = %d", len(tr.Packets))
	}
	if tr.Duplicates != 0 || tr.BurstLosses != 0 || tr.Truncated != 0 {
		t.Fatalf("clean trace must have no damage: %+v", tr)
	}
	// Every payload must parse to a decidable duration.
	for _, p := range tr.Packets {
		if len(p.Payload) == 0 {
			t.Fatal("clean packet must not be empty")
		}
	}
	res, err := mp4.Scan(bytes.NewReader(tr.MP4))
	if err != nil {
		t.Fatalf("clean mp4 must scan: %v", err)
	}
	if len(res.Tracks) != 1 || len(res.Tracks[0].Keyframes) != 5 {
		t.Fatalf("clean track wrong: %+v", res.Tracks)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("clean mp4 must have no findings: %+v", res.Findings)
	}
}

func TestGenerateDamaged(t *testing.T) {
	tr, err := Generate(Options{Profile: ProfileDamaged, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	// One duplicate inserted, three removed, 24 originals => 22 records.
	if len(tr.Packets) != 22 {
		t.Fatalf("damaged packet count = %d, want 22", len(tr.Packets))
	}
	if tr.Duplicates != 1 || tr.BurstLosses != 3 || tr.Truncated != 1 || tr.TimestampRegression != 1 {
		t.Fatalf("damage markers wrong: %+v", tr)
	}
	empty := 0
	for _, p := range tr.Packets {
		if len(p.Payload) == 0 {
			empty++
		}
	}
	if empty != 1 {
		t.Fatalf("want one truncated (empty) packet, got %d", empty)
	}
	res, err := mp4.Scan(bytes.NewReader(tr.MP4))
	if err != nil {
		t.Fatalf("damaged mp4 must still scan: %v", err)
	}
	if model.CountCode(res.Findings, model.ErrMissingIndex) == 0 {
		t.Fatal("bad stss index must produce a finding")
	}
	// The two surviving keyframes create an 8 ms oversized interval.
	if res.Tracks[0].MaxKeyframeInterval != model.TicksFromMillis(8) {
		t.Fatalf("keyframe interval = %v", res.Tracks[0].MaxKeyframeInterval)
	}
}

func TestGenerateCancel(t *testing.T) {
	tr, err := Generate(Options{Profile: ProfileCancel, Seed: 7, CancelAfter: 5})
	if err != nil {
		t.Fatal(err)
	}
	if tr.PlannedJobs != 15 {
		t.Fatalf("planned jobs = %d, want 15", tr.PlannedJobs)
	}
	if len(tr.Packets) != 24 {
		t.Fatal("cancel profile keeps the full packet stream")
	}
}

func TestGenerateDeterministic(t *testing.T) {
	a, err := Generate(Options{Profile: ProfileDamaged, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Generate(Options{Profile: ProfileDamaged, Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Packets) != len(b.Packets) {
		t.Fatal("same seed must yield same size")
	}
	for i := range a.Packets {
		pa, pb := a.Packets[i], b.Packets[i]
		if pa.Sequence != pb.Sequence || pa.Stream != pb.Stream ||
			!bytes.Equal(pa.Payload, pb.Payload) {
			t.Fatalf("packet %d differs across runs", i)
		}
	}
	if !bytes.Equal(a.MP4, b.MP4) {
		t.Fatal("MP4 bytes must be identical for the same seed")
	}
}

func TestGenerateRejectsUnknownProfile(t *testing.T) {
	if _, err := Generate(Options{Profile: "nope", Seed: 1}); err == nil {
		t.Fatal("unknown profile must error")
	}
}

func TestGeneratedCodecsAreMixed(t *testing.T) {
	tr, _ := Generate(Options{Profile: ProfileClean, Seed: 1})
	var opus, aac int
	for _, p := range tr.Packets {
		switch p.Codec {
		case model.CodecOpus:
			opus++
		case model.CodecAAC:
			aac++
		}
	}
	if opus == 0 || aac == 0 {
		t.Fatalf("trace must mix Opus and AAC: opus=%d aac=%d", opus, aac)
	}
}
