package remux

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"liveregress/internal/fault"
	"liveregress/internal/fmp4"
)

func TestRemuxPreservesSampleBytes(t *testing.T) {
	src := []byte("0123456789abcdefghijklmnopqrstuvwxyz")
	outPath := filepath.Join(t.TempDir(), "out.mp4")
	w, err := NewWriter(outPath, "src.bin", bytes.NewReader(src), Options{})
	if err != nil {
		t.Fatal(err)
	}
	tr := Track{InputID: 1, OutputID: 1, Timescale: 48000, Handler: "soun", Codec: "ipcm",
		Channels: 3, SampleSize: 24, SampleRate: 48000}
	tr.Samples = []Sample{
		{TrackID: 1, SourceOffset: 0, Size: 9, Duration: 1024, Keyframe: true},
		{TrackID: 1, SourceOffset: 9, Size: 9, Duration: 1024},
		{TrackID: 1, SourceOffset: 18, Size: 9, Duration: 1024},
	}
	if err := w.WriteInit([]Track{tr}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteFragment(FragmentPlan{Sequence: 1, TrackID: 1, BaseDecodeTime: 0,
		Samples: tr.Samples}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Re-parse the remuxed file and compare sample bytes against the source.
	initR, err := fmp4.Open(outPath, "out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	init, err := initR.ParseInit()
	if err != nil {
		t.Fatal(err)
	}
	seg, err := initR.ParseSegment()
	if err != nil {
		t.Fatal(err)
	}
	if len(init.Tracks) != 1 || init.Tracks[0].Channels != 3 || init.Tracks[0].SampleSize != 24 {
		t.Fatalf("init = %+v", init.Tracks)
	}
	samples := seg.Fragments[0].Samples
	if len(samples) != 3 {
		t.Fatalf("samples = %d", len(samples))
	}
	for i, s := range samples {
		got, err := initR.ReadSample(s, 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		want := src[i*9 : i*9+9]
		if !bytes.Equal(got, want) {
			t.Fatalf("sample %d bytes changed: got %q want %q", i, got, want)
		}
	}
	if samples[1].DecodeTime != 1024 || samples[0].Keyframe != true || samples[1].Keyframe != false {
		t.Fatalf("timing/flags wrong: %+v", samples)
	}
	if seg.Fragments[0].BaseDecodeTime != 0 || seg.Fragments[0].TrackID != 1 {
		t.Fatalf("fragment = %+v", seg.Fragments[0])
	}
	initR.Close()
}

func TestRemuxIsDeterministic(t *testing.T) {
	src := []byte("abcdefghijklmnopqrstuvwxyz0123456789")
	run := func() []byte {
		outPath := filepath.Join(t.TempDir(), "out.mp4")
		w, err := NewWriter(outPath, "src", bytes.NewReader(src), Options{})
		if err != nil {
			t.Fatal(err)
		}
		tr := Track{OutputID: 2, Timescale: 90000, Handler: "vide", Codec: "avc1", Width: 640, Height: 360}
		tr.Samples = []Sample{
			{SourceOffset: 0, Size: 10, Duration: 3000, Keyframe: true},
			{SourceOffset: 10, Size: 10, Duration: 3000},
		}
		if err := w.WriteInit([]Track{tr}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.WriteFragment(FragmentPlan{Sequence: 1, TrackID: 2, Samples: tr.Samples}); err != nil {
			t.Fatal(err)
		}
		w.Close()
		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	a, b := run(), run()
	if !bytes.Equal(a, b) {
		t.Fatal("identical input produced different output bytes")
	}
}

func TestRemuxRejectsTruncatedSource(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "out.mp4")
	w, err := NewWriter(outPath, "src", bytes.NewReader([]byte("short")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_, err = w.WriteFragment(FragmentPlan{Sequence: 1, TrackID: 1, Samples: []Sample{
		{SourceOffset: 2, Size: 100, Keyframe: true},
	}})
	if !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("want truncate, got %v", err)
	}
	if fe, _ := fault.As(err); fe == nil || fe.Recoverable {
		t.Fatalf("fault should be non-recoverable: %v", err)
	}
}

func TestRemuxRespectsByteLimit(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "out.mp4")
	w, err := NewWriter(outPath, "src", bytes.NewReader(make([]byte, 1000)), Options{MaxTotalBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	err = w.WriteInit([]Track{{OutputID: 1, Timescale: 48000, Handler: "soun", Codec: "ipcm", Channels: 1, SampleSize: 16, SampleRate: 48000}})
	if !errors.Is(err, fault.ErrLimit) {
		t.Fatalf("want limit, got %v", err)
	}
}

func TestRemuxWritesExplicitPayload(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "out.mp4")
	w, err := NewWriter(outPath, "ts", bytes.NewReader(nil), Options{})
	if err != nil {
		t.Fatal(err)
	}
	tr := Track{OutputID: 1, Timescale: 90000, Handler: "vide", Codec: "avc1", Width: 640, Height: 360,
		Samples: []Sample{
			{SourceOffset: 999, Size: 4, Duration: 3000, Keyframe: true, Payload: []byte{0, 0, 0, 1}},
			{SourceOffset: 1000, Size: 3, Duration: 3000, Payload: []byte{9, 9, 9}},
		}}
	if err := w.WriteInit([]Track{tr}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteFragment(FragmentPlan{Sequence: 1, TrackID: 1, Samples: tr.Samples}); err != nil {
		t.Fatalf("explicit payload should not touch the source: %v", err)
	}
	w.Close()
	r, err := fmp4.Open(outPath, "out.mp4")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	seg, err := r.ParseSegment()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadSample(seg.Fragments[0].Samples[0], 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string([]byte{0, 0, 0, 1}) {
		t.Fatalf("payload = %v", got)
	}
}

func TestRemuxRejectsPayloadSizeMismatch(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "out.mp4")
	w, err := NewWriter(outPath, "ts", bytes.NewReader(nil), Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	_, err = w.WriteFragment(FragmentPlan{Sequence: 1, TrackID: 1, Samples: []Sample{
		{Size: 10, Payload: []byte{1, 2}, Keyframe: true},
	}})
	if !errors.Is(err, fault.ErrCorrupt) {
		t.Fatalf("want corrupt, got %v", err)
	}
}
