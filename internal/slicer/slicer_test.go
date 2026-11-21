package slicer

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hlsflow/internal/fixture"
	"hlsflow/internal/ts"
)

func TestCutterOneIDRPerSourceSegment(t *testing.T) {
	data := fixture.SampleSegment(0, 0)
	p := ts.NewParser(bytes.NewReader(data))
	c := NewCutter(p)
	// Target ~2s; the fixture places one IDR per ~2.44s source segment, so a
	// single cut is the nearest-boundary result.
	cuts, first, last, err := c.Run(2 * ts.TicksPerSecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(cuts) != 1 {
		t.Fatalf("cuts = %d, want 1", len(cuts))
	}
	if first < 0 || last <= first {
		t.Fatalf("bad pts first=%d last=%d", first, last)
	}
	if len(cuts[0].Data)%ts.PacketSize != 0 {
		t.Fatal("cut output not packet aligned")
	}
	// Each emitted segment must begin with the PAT.
	if cuts[0].Data[0] != 0x47 {
		t.Fatal("cut missing sync byte")
	}
	// The cut must be independently parseable with its own PSI.
	p2 := ts.NewParser(bytes.NewReader(cuts[0].Data))
	for {
		_, err := p2.Read()
		if errors.Is(err, ts.ErrTruncated) {
			t.Fatal(err)
		}
		if err != nil {
			break
		}
	}
	if err := p2.ValidatePSI(); err != nil {
		t.Fatalf("cut lacks PSI: %v", err)
	}
}

func TestCutterMultipleCuts(t *testing.T) {
	// One continuous muxer stream with three IDRs (~2.44s apart) keeps PTS
	// and continuity counters valid across cut points.
	data := fixture.ContinuousStream(3)
	p := ts.NewParser(bytes.NewReader(data))
	c := NewCutter(p)
	cuts, _, _, err := c.Run(2 * ts.TicksPerSecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(cuts) != 3 {
		t.Fatalf("cuts = %d, want 3", len(cuts))
	}
	prev := int64(-1)
	for _, cut := range cuts {
		if cut.StartPTS <= prev {
			t.Fatal("cuts not in monotonic PTS order")
		}
		if cut.DurationSeconds() <= 0 {
			t.Fatal("non-positive cut duration")
		}
		prev = cut.StartPTS
	}
}

func TestDiscoverAndReorder(t *testing.T) {
	dir := t.TempDir()
	// Three well-formed names plus a .ts file whose sequence is unparsable.
	names := []string{"seg-00002.ts", "seg-00000.ts", "seg-00001.ts", "notes.ts.txt", "broken-name.ts"}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte{0x47}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	files, rejects, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[0].Seq != 0 || files[2].Seq != 2 {
		t.Fatalf("discover order wrong: %+v", files)
	}
	if len(rejects) != 1 || rejects[0] != "broken-name.ts" {
		t.Fatalf("rejects = %v", rejects)
	}
	// Arrival order 2,0,1 vs sorted 0,1,2: all three positions differ.
	if n := ReorderedCount([]int{2, 0, 1}, files); n != 3 {
		t.Fatalf("reordered = %d", n)
	}
}

func TestWaitForGap(t *testing.T) {
	dir := t.TempDir()
	if WaitForGap(dir, 4, 60*time.Millisecond) {
		t.Fatal("gap should time out when file never arrives")
	}
	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = os.WriteFile(filepath.Join(dir, "seg-00004.ts"), []byte{0x47}, 0o600)
	}()
	if !WaitForGap(dir, 4, time.Second) {
		t.Fatal("gap did not resolve after file arrived")
	}
}
