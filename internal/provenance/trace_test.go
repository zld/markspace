package provenance

import (
	"testing"

	"example.com/continuity/internal/fault"
)

func TestConfigDigestDeterministic(t *testing.T) {
	cfg := map[string]any{"fps": 25, "window": 4, "tracks": []string{"v", "a"}}
	a, err := ConfigDigestOf(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := ConfigDigestOf(cfg)
	if a != b {
		t.Fatal("config digest not deterministic")
	}
	c, _ := ConfigDigestOf(map[string]any{"fps": 30, "window": 4})
	if a == c {
		t.Fatal("different config produced same digest")
	}
}

func TestTraceWriteAndVerify(t *testing.T) {
	tr := New("cfg-v1", "input-abc", "cfg-digest-1")
	tr.AddSource("frames", "in/frames.bin", "input-abc", 4096)
	tr.AddSource("rtp", "in/audio.bin", "rtp-digest", 1024)
	tr.AddOutput("segments.json", fault.StageSegment, 0, 12, "seg-digest")
	tr.AddOutput("dedup.json", fault.StageFingerprint, 0, 12, "dedup-digest")

	dir := t.TempDir()
	data, digest, err := tr.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	if digest == "" || tr.TraceChecksum == "" {
		t.Fatal("digests missing")
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Verify(); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
	if len(got.Outputs) != 2 || got.Outputs[0].InputDigest != "input-abc" {
		t.Fatalf("output source mapping wrong: %+v", got.Outputs)
	}
	// Deterministic bytes across rewrites.
	data2, _, err := tr.Write(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(data2) {
		t.Fatal("trace bytes not deterministic")
	}
}

func TestTraceDetectsTampering(t *testing.T) {
	tr := New("cfg-v1", "input-abc", "cfg-digest-1")
	tr.AddOutput("segments.json", fault.StageSegment, 0, 1, "d")
	dir := t.TempDir()
	if _, _, err := tr.Write(dir); err != nil {
		t.Fatal(err)
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Tamper with an output source's input digest.
	got.Outputs[0].InputDigest = "tampered"
	if err := got.Verify(); err == nil {
		t.Fatal("tampered trace accepted")
	}
	// Checksum tampering is also rejected.
	got2, _ := Read(dir)
	got2.TraceChecksum = "00"
	if err := got2.Verify(); err == nil {
		t.Fatal("bad checksum accepted")
	}
}
