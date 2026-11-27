package demux_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/demux"
	"github.com/zld/continuity/internal/mux"
)

func makeFile(t *testing.T, tracks []mux.TrackSpec, fault mux.Faults) string {
	t.Helper()
	f, err := os.Create(filepath.Join(t.TempDir(), "in.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := mux.WriteFile(f, tracks, fault); err != nil && fault == mux.FaultNone {
		t.Fatal(err)
	}
	return f.Name()
}

func video(n, every int) mux.TrackSpec {
	specs := make([]mux.SampleSpec, n)
	for i := range specs {
		specs[i] = mux.SampleSpec{Delta: 1, Sync: every > 0 && i%every == 0}
	}
	return mux.TrackSpec{Timescale: 25, Video: true, Samples: specs}
}

func TestOpenAndReadSamples(t *testing.T) {
	path := makeFile(t, []mux.TrackSpec{video(20, 5)}, mux.FaultNone)
	f, err := demux.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Path() != path || len(f.Tracks) != 1 {
		t.Fatalf("file handle wrong: %+v", f)
	}
	tr := f.Tracks[0]
	if len(tr.SyncRows) != 4 {
		t.Fatalf("sync rows = %d, want 4", len(tr.SyncRows))
	}
	// Independent re-read of every sample through the public API.
	for i := range tr.Samples {
		raw, err := f.ReadSample(0, uint32(i))
		if err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
		if len(raw) != int(tr.Samples[i].Size) {
			t.Fatalf("sample %d length mismatch", i)
		}
	}
	// DTS must be exact rationals on the declared 25 Hz timescale.
	if tr.Samples[7].DTS.Cmp(clock.MustMake(7, 25)) != 0 {
		t.Fatalf("dts = %s", tr.Samples[7].DTS)
	}
}

func TestReadSampleOutOfRangeReportsLocation(t *testing.T) {
	path := makeFile(t, []mux.TrackSpec{video(4, 2)}, mux.FaultNone)
	f, _ := demux.Open(path)
	defer f.Close()

	if _, err := f.ReadSample(9, 0); !errors.Is(err, clock.ErrCorrupt) {
		t.Fatalf("unknown track: want ErrCorrupt, got %v", err)
	}
	_, err := f.ReadSample(0, 40)
	var ce *clock.Error
	if !errors.As(err, &ce) || !ce.KnownT || !ce.KnownS || ce.Sample != 40 {
		t.Fatalf("out-of-range must report track and sample: %v", err)
	}
}

func TestCorruptInputsRejectedWithSentinel(t *testing.T) {
	cases := map[string]mux.Faults{
		"truncated":     mux.FaultTruncateStbl,
		"bad-stts":      mux.FaultBadSttsCount,
		"no-timescale":  mux.FaultZeroTimescale,
	}
	for name, fault := range cases {
		t.Run(name, func(t *testing.T) {
			path := makeFile(t, []mux.TrackSpec{video(10, 2)}, fault)
			_, err := demux.Open(path)
			if err == nil {
				t.Fatal("expected rejection")
			}
			if !errors.Is(err, clock.ErrCorrupt) {
				t.Fatalf("want ErrCorrupt, got %v", err)
			}
		})
	}
}

func TestMissingFileIsDemuxError(t *testing.T) {
	if _, err := demux.Open(filepath.Join(t.TempDir(), "nope.mp4")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestAudioTrackParsed(t *testing.T) {
	audio := mux.TrackSpec{
		Timescale: 48000,
		Samples: func() []mux.SampleSpec {
			out := make([]mux.SampleSpec, 12)
			for i := range out {
				out[i] = mux.SampleSpec{Delta: 1024}
			}
			return out
		}(),
	}
	path := makeFile(t, []mux.TrackSpec{video(12, 6), audio}, mux.FaultNone)
	f, err := demux.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if len(f.Tracks) != 2 {
		t.Fatalf("tracks = %d", len(f.Tracks))
	}
	if f.Tracks[0].Video != true || f.Tracks[1].Video != false {
		t.Fatal("handler type not distinguished")
	}
	if f.Tracks[1].Timescale != 48000 || len(f.Tracks[1].Samples) != 12 {
		t.Fatal("audio table wrong")
	}
	if f.Tracks[1].DurationTicks() != 12*1024 {
		t.Fatalf("audio duration ticks = %d", f.Tracks[1].DurationTicks())
	}
}
