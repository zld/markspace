package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediaroute/internal/clock"
	"mediaroute/internal/journal"
	"mediaroute/internal/transport"
)

func TestCrashBeforeCommitRollsBackAndRerunMatches(t *testing.T) {
	opts := demoOptions(t)
	cleanHash := runHashNoCleanup(t, withTempDir(t, opts))

	for _, point := range []string{"wal-begin", "segment-commit", "index-commit"} {
		t.Run(point, func(t *testing.T) {
			crashOpts := opts
			crashOpts.JournalDir = filepath.Join(t.TempDir(), "crash-"+point)
			crashOpts.CrashAt = point
			if _, err := NewRunner(crashOpts); err != nil {
				t.Fatal(err)
			}
			// The runner that crashed must have reported a journal error.
			if err := crashRun(crashOpts); err == nil {
				t.Fatal("crash injection did not surface")
			}
			report, err := journal.Recover(crashOpts.JournalDir)
			if err != nil {
				t.Fatal(err)
			}
			if report.Isolated {
				t.Fatal("clean crash must not need tail isolation")
			}
			// Rerun over the recovered journal with no crash injection.
			rerun := crashOpts
			rerun.CrashAt = ""
			got := runHashNoCleanup(t, rerun)
			if got != cleanHash {
				t.Fatalf("recovered rerun hash %s != clean %s", got, cleanHash)
			}
		})
	}
}

// withTempDir gives opts a fresh journal directory.
func withTempDir(t *testing.T, opts Options) Options {
	t.Helper()
	opts.JournalDir = filepath.Join(t.TempDir(), "clean")
	return opts
}

func crashRun(opts Options) error {
	r, err := NewRunner(opts)
	if err != nil {
		return err
	}
	_, err = r.Run()
	return err
}

func TestCorruptIndexFailsWithIndexPhase(t *testing.T) {
	opts := demoOptions(t)
	opts.CorruptIndex = true
	r, err := NewRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Run()
	if err == nil {
		t.Fatal("corrupted index was accepted")
	}
	pe, ok := clock.AsError(err)
	if !ok || pe.Phase != clock.PhaseIndex || pe.Recoverable {
		t.Fatalf("corrupt index error model wrong: %v", err)
	}
	if !strings.Contains(pe.Message, "refusing to repair") {
		t.Fatalf("corruption must refuse repair: %q", pe.Message)
	}
}

func TestCorruptStreamFailsWithCodecPhase(t *testing.T) {
	opts := demoOptions(t)
	opts.CorruptStream = true
	r, err := NewRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Run(); err == nil {
		t.Fatal("corrupted stream was accepted")
	}
	pe, ok := clock.AsError(err)
	if !ok || pe.Phase != clock.PhaseCodec || pe.Recoverable {
		t.Fatalf("corrupt stream error model wrong: %v", err)
	}
}

func TestCorruptTailIsolatedThenRecovered(t *testing.T) {
	opts := demoOptions(t)
	clean := runHashNoCleanup(t, withTempDir(t, opts))

	// Produce a real journal, then corrupt its tail as a torn write would.
	if err := crashRunWithoutCrash(opts); err != nil {
		t.Fatal(err)
	}
	walPath := filepath.Join(opts.JournalDir, "journal.wal")
	f, err := os.OpenFile(walPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0x03, 0x00, 0x10, 0x00, 0x00, 0x00, 0x00}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	report, err := journal.Recover(opts.JournalDir)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Isolated {
		t.Fatal("torn WAL tail was not isolated")
	}
	// The committed prefix must still replay and the rerun must be identical.
	if len(report.Replayed) != SegmentCount+3 {
		t.Fatalf("replayed %d stages, want %d", len(report.Replayed), SegmentCount+3)
	}
	rerun := opts
	rerun.CrashAt = ""
	if got := runHashNoCleanup(t, rerun); got != clean {
		t.Fatalf("post-isolation rerun hash %s != clean %s", got, clean)
	}
}

func crashRunWithoutCrash(opts Options) error {
	opts.CrashAt = ""
	return crashRun(opts)
}

func TestUnknownCodecIsUnsupported(t *testing.T) {
	opts := demoOptions(t)
	r, err := NewRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	// Route an unknown SSRC through the codec stage directly.
	frames := map[uint32][]*transport.Frame{99: {{SSRC: 99, Bytes: []byte{0}}}}
	_, _, _, _, err = r.runCodecAndIndex(frames)
	if err == nil {
		t.Fatal("unknown codec SSRC accepted")
	}
	pe, ok := clock.AsError(err)
	if !ok || pe.Phase != clock.PhaseCodec || pe.Recoverable {
		t.Fatalf("unknown codec error model wrong: %v", err)
	}
}

// TestSamplesTrackSourceOrder proves the exported sample numbers line up with
// the keyframe index: each track's samples are 1..N in timeline order, and
// the only gap is the packet the loss burst removed.
func TestSamplesTrackSourceOrder(t *testing.T) {
	opts := demoOptions(t)
	r, err := NewRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); err != nil {
		t.Fatal(err)
	}
	last := map[uint32]uint32{}
	for _, rec := range r.Manifest().Records {
		if rec.Sample <= last[rec.Track] {
			t.Fatalf("track %d sample %d does not follow %d",
				rec.Track, rec.Sample, last[rec.Track])
		}
		last[rec.Track] = rec.Sample
	}
	// Every track has 50 source samples; only track 10 loses one to the burst.
	for _, ssrc := range []uint32{10, 20, 30} {
		want := uint32(50)
		if ssrc == 10 {
			want = 49
		}
		if last[ssrc] != want {
			t.Fatalf("track %d exported %d samples want %d", ssrc, last[ssrc], want)
		}
	}
}

// TestTimelineIsMonotonicPerTrack ensures unified-clock mapping never runs
// backwards for a track, including across the AV1 track's 32-bit wrap.
func TestTimelineIsMonotonicPerTrack(t *testing.T) {
	opts := demoOptions(t)
	r, err := NewRunner(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); err != nil {
		t.Fatal(err)
	}
	prev := map[uint32]int64{}
	wrapped := false
	for _, f := range r.Timeline().Frames {
		if u, ok := prev[f.Track]; ok {
			if f.Usec <= u {
				t.Fatalf("track %d usec not increasing: %d -> %d",
					f.Track, u, f.Usec)
			}
			if f.Track == 30 && f.RTPTS < 0xFFFF {
				wrapped = true
			}
		}
		prev[f.Track] = f.Usec
	}
	if !wrapped {
		t.Fatal("AV1 track never crossed its 32-bit timestamp wrap")
	}
}
