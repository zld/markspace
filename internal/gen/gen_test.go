package gen

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"mediarun/internal/frames"
	rtpparse "mediarun/internal/rtp"
)

func TestGenerateIsDeterministic(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	ra, err := Generate(Options{Dir: a})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := Generate(Options{Dir: b})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"frames.mfr", "audio.rtp", "audio.stats"} {
		da, _ := os.ReadFile(filepath.Join(a, name))
		db, _ := os.ReadFile(filepath.Join(b, name))
		if !bytes.Equal(da, db) {
			t.Fatalf("%s differs between runs", name)
		}
	}
	if ra.Frames != rb.Frames || ra.Packets != rb.Packets || ra.BaseRTP != rb.BaseRTP {
		t.Fatalf("results differ: %+v vs %+v", ra, rb)
	}
}

func TestGenerateHealthyFixtureScansClean(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(Options{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(res.FramesPath)
	defer f.Close()
	info, _ := f.Stat()
	sc, err := frames.NewScanner(f, info.Size(), frames.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	var recs []*frames.Record
	for {
		rec, err := sc.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	if len(recs) != res.Frames {
		t.Fatalf("records = %d want %d", len(recs), res.Frames)
	}
	if sc.Faults().Len() != 0 {
		t.Fatalf("healthy fixture has faults: %s", sc.Faults().Summary())
	}
	if !recs[0].Keyframe() {
		t.Fatal("first record is not a keyframe")
	}
	if !recs[len(recs)-1].EndOfSequence() {
		t.Fatal("last record missing end-of-sequence")
	}
}

func TestGenerateChecksumAndTruncationOffsets(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(Options{Dir: dir, Inject: []string{"truncated", "checksum"}})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(res.FramesPath)
	defer f.Close()
	info, _ := f.Stat()
	sc, _ := frames.NewScanner(f, info.Size(), frames.Limits{})
	for {
		if _, err := sc.Next(); err != nil {
			break
		}
	}
	fs := sc.Faults()
	if fs.CountKind("checksum_mismatch") != 1 {
		t.Fatalf("faults = %s", fs.Summary())
	}
	if fs.CountKind("truncated_record") != 1 {
		t.Fatalf("faults = %s", fs.Summary())
	}
	if off, ok := fs.FirstOffset(); !ok || off != res.InjectedOffsets[2] {
		t.Fatalf("first offset = %d want %d", off, res.InjectedOffsets[2])
	}
}

func TestGenerateSeqWrapFixture(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(Options{Dir: dir, Inject: []string{"seqwrap"}})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(res.AudioPath)
	defer f.Close()
	info, _ := f.Stat()
	pkts, faults := rtpparse.ParseSidecar(f, info.Size())
	if faults.Len() != 0 {
		t.Fatalf("parse faults: %s", faults.Summary())
	}
	var w rtpparse.SequenceWindow
	wrapped := false
	prev := uint64(0)
	for _, p := range pkts {
		ext := w.Extend(p.Sequence)
		if p.Sequence < 100 && prev > 60000 {
			wrapped = true
		}
		if ext < prev {
			t.Fatalf("extended sequence went backwards: %d -> %d", prev, ext)
		}
		prev = ext
	}
	if !wrapped {
		t.Fatal("seqwrap fixture did not actually wrap")
	}
}

func TestGenerateOverflowFixtureExceedsWindow(t *testing.T) {
	dir := t.TempDir()
	res, err := Generate(Options{Dir: dir, Inject: []string{"overflow"}})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(res.AudioPath)
	defer f.Close()
	info, _ := f.Stat()
	pkts, _ := rtpparse.ParseSidecar(f, info.Size())
	var w rtpparse.SequenceWindow
	reversals := 0
	prev := -1
	for _, p := range pkts {
		w.Extend(p.Sequence)
		if prev >= 0 && int(p.Sequence) < prev {
			reversals++
		}
		prev = int(p.Sequence)
	}
	if reversals == 0 {
		t.Fatal("overflow fixture has no reordering")
	}
}

func TestGenerateRejectsUnknownInjection(t *testing.T) {
	if _, err := Generate(Options{Dir: t.TempDir(), Inject: []string{"nope"}}); err == nil {
		t.Fatal("unknown injection accepted")
	}
	if err := ValidateInjections([]string{"gap", "dupe"}); err != nil {
		t.Fatalf("known injections rejected: %v", err)
	}
}
