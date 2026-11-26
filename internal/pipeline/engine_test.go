package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestColdWarmMetrics(t *testing.T) {
	dir := t.TempDir()
	eng, err := NewEngine(dir, 19)
	if err != nil {
		t.Fatal(err)
	}
	m, err := eng.Run()
	if err != nil {
		t.Fatal(err)
	}
	if m.ColdRebuilt != 8 {
		t.Fatalf("cold_rebuilt=%d, want 8", m.ColdRebuilt)
	}
	if m.WarmRebuilt != 0 {
		t.Fatalf("warm_rebuilt=%d, want 0", m.WarmRebuilt)
	}
	if m.FaultOffsetDelta != 0 {
		t.Fatalf("fault_offset_delta=%d, want 0", m.FaultOffsetDelta)
	}
	if m.RTPDuplicate != 2 || m.RTPReordered != 3 || m.RTPBurstLost != 4 {
		t.Fatalf("rtp metrics wrong: dup=%d reo=%d burst=%d",
			m.RTPDuplicate, m.RTPReordered, m.RTPBurstLost)
	}
	if m.ResumeMS > 2000 {
		t.Fatalf("resume_ms=%d exceeds budget", m.ResumeMS)
	}
	if len(m.ManifestSHA256) != 0 && m.ManifestSHA256[:9] != "manifest:" {
		t.Fatalf("manifest digest shape wrong: %s", m.ManifestSHA256)
	}
	if len(m.Breaks) != 0 {
		t.Fatalf("unexpected continuity breaks: %v", m.Breaks)
	}
}

func TestManifestFileStableAcrossEngines(t *testing.T) {
	dir1, dir2 := t.TempDir(), t.TempDir()
	e1, err := NewEngine(dir1, 19)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e1.Run(); err != nil {
		t.Fatal(err)
	}
	e2, err := NewEngine(dir2, 19)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := e2.Run()
	if err != nil {
		t.Fatal(err)
	}
	b1, err := os.ReadFile(filepath.Join(dir1, "out", "manifest.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	b2, err := os.ReadFile(filepath.Join(dir2, "out", "manifest.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b1) != string(b2) {
		t.Fatal("manifest bytes differ across independent engines")
	}
	if m2.WarmRebuilt != 0 {
		t.Fatalf("second engine warm rebuilds=%d, want 0", m2.WarmRebuilt)
	}
}

func TestInterruptResumesToIdenticalManifest(t *testing.T) {
	dir := t.TempDir()
	eng, err := NewEngine(dir, 19)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, _, sha, err := eng.RunInterrupt()
	if err != nil {
		t.Fatal(err)
	}
	// Two records were dropped/tampered, so resume recomputes them.
	if rebuilt != 2 {
		t.Fatalf("resume rebuilt=%d, want 2", rebuilt)
	}
	after, err := os.ReadFile(filepath.Join(dir, "out", "manifest.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	// A fresh engine over the same work dir must fully reuse and converge.
	eng2, err := NewEngine(dir, 19)
	if err != nil {
		t.Fatal(err)
	}
	m, err := eng2.Run()
	if err != nil {
		t.Fatal(err)
	}
	if m.WarmRebuilt != 0 {
		t.Fatalf("post-resume warm rebuilds=%d, want 0", m.WarmRebuilt)
	}
	again, err := os.ReadFile(filepath.Join(dir, "out", "manifest.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(again) {
		t.Fatal("manifest changed after resume convergence")
	}
	if want := "manifest:"; len(sha) < len(want) || sha[:len(want)] != want {
		t.Fatalf("interrupt digest shape wrong: %s", sha)
	}
}

func TestDeterministicAcrossSeeds(t *testing.T) {
	a, err := NewEngine(t.TempDir(), 19)
	if err != nil {
		t.Fatal(err)
	}
	ma, err := a.Run()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewEngine(t.TempDir(), 19)
	if err != nil {
		t.Fatal(err)
	}
	mb, err := b.Run()
	if err != nil {
		t.Fatal(err)
	}
	if ma.ManifestSHA256 != mb.ManifestSHA256 {
		t.Fatal("same seed produced different manifest digest")
	}
}
