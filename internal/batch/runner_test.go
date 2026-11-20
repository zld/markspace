package batch

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"loudscan/internal/ebu"
	"loudscan/internal/media"
	"loudscan/internal/report"
)

// fakeMedia installs probe/decode seams whose behaviour is driven by content
// markers embedded in the file name, so tests never need real audio.
func fakeMedia(r *Runner) {
	r.probe = func(ctx context.Context, path string, opts media.Options) (media.Info, error) {
		base := filepath.Base(path)
		switch {
		case contains(base, "noaudio"):
			return media.Info{}, &media.ProbeError{Stage: "probe", Reason: "no audio stream"}
		case contains(base, "weird"):
			return media.Info{}, &media.ProbeError{Stage: "probe", Reason: "unsupported channel layout"}
		}
		layout, _ := ebu.NewLayout([]string{"FL", "FR"})
		return media.Info{SampleRate: 48000, Channels: 2, Layout: layout, RawLayout: "stereo", Duration: 1}, nil
	}
	r.decode = func(ctx context.Context, path string, info media.Info, opts media.DecodeOptions, sink media.Sink) error {
		base := filepath.Base(path)
		if contains(base, "broken") {
			return &media.DecodeError{Stage: "decode", Reason: "ffmpeg failed"}
		}
		amp := math.Pow(10, (-23+0.691)/20) * math.Sqrt(2)
		if contains(base, "loud") {
			amp = math.Pow(10, (-5+0.691)/20) * math.Sqrt(2)
		}
		n := 48000
		frames := make([]float64, n*2)
		for i := 0; i < n; i++ {
			v := amp * math.Sin(2*math.Pi*1000*float64(i)/48000)
			frames[i*2] = v
			frames[i*2+1] = v
		}
		return sink(frames)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func write(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func TestCollectDedupesAndSorts(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "b.wav"))
	write(t, filepath.Join(dir, "a.wav"))
	write(t, filepath.Join(dir, "notes.txt"))
	write(t, filepath.Join(dir, "sub", "c.flac"))
	if err := os.Symlink(filepath.Join(dir, "sub"), filepath.Join(dir, "loop")); err != nil {
		t.Skipf("symlink: %v", err)
	}

	col, err := Collect([]string{dir, filepath.Join(dir, "a.wav"), dir})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a.wav", "b.wav", "sub/c.flac"}
	if len(col.Files) != len(want) {
		t.Fatalf("files = %v, want %v", col.Files, want)
	}
	for i, f := range col.Files {
		if f.Display != want[i] {
			t.Fatalf("files[%d] = %q, want %q (order: %v)", i, f.Display, want[i], col.Files)
		}
	}
	// The explicit a.wav and the walked one must appear once only.
	seen := map[string]int{}
	for _, f := range col.Files {
		seen[f.Display]++
	}
	if seen["a.wav"] != 1 {
		t.Errorf("a.wav appears %d times", seen["a.wav"])
	}
	// Directory symlink must not be walked twice.
	for _, f := range col.Files {
		if contains(f.Display, "loop") {
			t.Errorf("directory symlink was followed: %s", f.Display)
		}
	}
}

func TestCollectReportsMissingInput(t *testing.T) {
	col, err := Collect([]string{filepath.Join(t.TempDir(), "nope")})
	if err != nil {
		t.Fatal(err)
	}
	if len(col.Errs) != 1 || col.Errs[0].Stage != "discover" {
		t.Fatalf("errs = %+v", col.Errs)
	}
}

func TestRunContinuesAfterFailures(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"good.wav", "broken.wav", "noaudio.wav", "loud.wav", "weird.wav"} {
		write(t, filepath.Join(dir, name))
	}
	col, err := Collect([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	r := New(Options{IntegratedMax: -16, TruePeakMax: -1, Workers: 2})
	fakeMedia(r)
	entries, err := r.Run(context.Background(), col)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(entries))
	}
	byStatus := map[report.Status]int{}
	for _, e := range entries {
		byStatus[e.Status]++
		if e.Status == report.StatusError && e.ErrorStage == "" {
			t.Errorf("%s has no error stage", e.Path)
		}
	}
	if byStatus[report.StatusPass] != 1 || byStatus[report.StatusLoudnessFail] != 1 || byStatus[report.StatusError] != 3 {
		t.Errorf("statuses = %v", byStatus)
	}
	rep := report.Build(entries)
	if rep.Summary.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", rep.Summary.ExitCode)
	}
}

func TestRunRespectsWorkerLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 24; i++ {
		write(t, filepath.Join(dir, "f"+strconv.Itoa(i)+".wav"))
	}
	col, _ := Collect([]string{dir})
	r := New(Options{IntegratedMax: -16, TruePeakMax: -1, Workers: 3})
	fakeMedia(r)
	var live, maxLive int32
	r.decode = func(ctx context.Context, path string, info media.Info, opts media.DecodeOptions, sink media.Sink) error {
		n := atomic.AddInt32(&live, 1)
		for {
			m := atomic.LoadInt32(&maxLive)
			if n <= m || atomic.CompareAndSwapInt32(&maxLive, m, n) {
				break
			}
		}
		defer atomic.AddInt32(&live, -1)
		return sink(make([]float64, 4800*2))
	}
	if _, err := r.Run(context.Background(), col); err != nil {
		t.Fatal(err)
	}
	if maxLive > 3 {
		t.Errorf("max concurrent decodes = %d, want <= 3", maxLive)
	}
}

func TestRunSilenceIsNoMeasurement(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "silence.wav"))
	col, _ := Collect([]string{dir})
	r := New(Options{IntegratedMax: -16, TruePeakMax: -1, Workers: 1})
	fakeMedia(r)
	r.decode = func(ctx context.Context, path string, info media.Info, opts media.DecodeOptions, sink media.Sink) error {
		return sink(make([]float64, 48000*2))
	}
	entries, _ := r.Run(context.Background(), col)
	if len(entries) != 1 || entries[0].Status != report.StatusNoMeasurement {
		t.Fatalf("entries = %+v", entries)
	}
	if entries[0].IntegratedLUFS.Valid {
		t.Error("silence reported a valid integrated level")
	}
	if report.Build(entries).Summary.ExitCode != 1 {
		t.Error("no_measurement must not exit 0")
	}
}

func TestRunCancelledListsEveryFile(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a.wav", "b.wav", "c.wav"} {
		write(t, filepath.Join(dir, n))
	}
	col, _ := Collect([]string{dir})
	r := New(Options{IntegratedMax: -16, TruePeakMax: -1, Workers: 1})
	fakeMedia(r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	entries, err := r.Run(ctx, col)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	for _, e := range entries {
		if e.Status != report.StatusError {
			t.Errorf("%s status = %s", e.Path, e.Status)
		}
	}
}
