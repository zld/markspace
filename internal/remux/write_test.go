package remux_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/maptrack"
	"mediacore/internal/remux"
	"mediacore/internal/scan"
)

func testIndex() *scan.Index {
	p := scan.DefaultParams()
	return &scan.Index{
		Version: scan.IndexVersion, SourcePath: "", Format: scan.FormatMP4,
		SourceSHA256: "fixture-digest", Params: p,
		Tracks: []scan.Track{
			{ID: 1, Handler: "vide", Kind: "video", Codec: "mp4v", Timescale: 24000, Width: 160, Height: 120},
			{ID: 2, Handler: "soun", Kind: "audio", Codec: "mp4a", Timescale: 48000, Channels: 2, SampleRate: 48000, BitsPerSample: 16},
		},
	}
}

func testSamples() []scan.SampleRef {
	var out []scan.SampleRef
	vTB := clock.Timebase{Timescale: 24000}
	aTB := clock.Timebase{Timescale: 48000}
	for i := uint32(0); i < 6; i++ {
		data := []byte{byte(i + 1), 'V', 0, 0, byte(i * 7)}
		dts := int64(i) * 1000
		out = append(out, scan.SampleRef{
			TrackID: 1, Index: i, Size: uint32(len(data)), Data: data,
			DTSUnits: dts, PTSUnits: dts, DurationUnits: 1000,
			DTSNS: vTB.ToNS(dts), PTSNS: vTB.ToNS(dts), KeyFrame: true,
		})
	}
	for i := uint32(0); i < 8; i++ {
		data := []byte{'A', byte(i + 1), 0, 0, 0, byte(i * 11)}
		dts := int64(i) * 6000 // 8 audio blocks span the same 0.5s test window
		out = append(out, scan.SampleRef{
			TrackID: 2, Index: i, Size: uint32(len(data)), Data: data,
			DTSUnits: dts, PTSUnits: dts, DurationUnits: 6000,
			DTSNS: aTB.ToNS(dts), PTSNS: aTB.ToNS(dts), KeyFrame: true,
		})
	}
	return out
}

func TestWriteMP4LosslessAndMonotonic(t *testing.T) {
	dir := t.TempDir()
	idx := testIndex()
	samples := testSamples()
	mappings, err := maptrack.Map(idx)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "lossless.mp4")
	res, err := remux.WriteMP4(idx, mappings, samples, remux.Options{OutputPath: out, AllowedRoot: dir, Resume: true})
	if err != nil {
		t.Fatalf("WriteMP4: %v", err)
	}
	if res.SampleCount != len(samples) {
		t.Fatalf("sample count=%d want %d", res.SampleCount, len(samples))
	}

	reIdx, err := scan.BuildIndex(out, scan.DefaultParams())
	if err != nil {
		t.Fatalf("rescan output: %v", err)
	}
	if len(reIdx.Samples) != len(samples) {
		t.Fatalf("rescanned samples=%d want %d", len(reIdx.Samples), len(samples))
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	want := map[uint32]map[uint32][]byte{}
	for _, s := range samples {
		if want[s.TrackID] == nil {
			want[s.TrackID] = map[uint32][]byte{}
		}
		want[s.TrackID][s.Index] = s.Data
	}
	got := map[uint32]map[uint32][]byte{}
	last := map[uint32]int64{}
	for _, s := range reIdx.Samples {
		if s.Offset+int64(s.Size) > int64(len(raw)) {
			t.Fatalf("sample outside output: %+v", s)
		}
		payload := append([]byte(nil), raw[s.Offset:s.Offset+int64(s.Size)]...)
		if got[s.TrackID] == nil {
			got[s.TrackID] = map[uint32][]byte{}
		}
		got[s.TrackID][s.Index] = payload
		if prev, ok := last[s.TrackID]; ok && s.DTSNS < prev {
			t.Fatalf("track %d DTS went backwards: %d < %d", s.TrackID, s.DTSNS, prev)
		}
		last[s.TrackID] = s.DTSNS
	}
	for track, byIndex := range want {
		for i, w := range byIndex {
			if string(got[track][i]) != string(w) {
				t.Fatalf("track %d sample %d payload changed", track, i)
			}
		}
	}
}

func TestWriteMP4ResumeFromSampleBoundary(t *testing.T) {
	dir := t.TempDir()
	idx := testIndex()
	samples := testSamples()
	mappings, err := maptrack.Map(idx)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "resume.mp4")
	_, err = remux.WriteMP4(idx, mappings, samples, remux.Options{
		OutputPath: out, AllowedRoot: dir, Resume: true,
		AfterSample: func(n int) error {
			if n == 3 {
				return remux.ErrInterrupt
			}
			return nil
		},
	})
	if !errors.Is(err, remux.ErrInterrupt) {
		t.Fatalf("first run err=%v want ErrInterrupt", err)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("final output must not exist after interrupt, stat err=%v", err)
	}
	if _, err := os.Stat(out + ".partial"); err != nil {
		t.Fatalf("partial output should remain: %v", err)
	}

	res, err := remux.WriteMP4(idx, mappings, samples, remux.Options{
		OutputPath: out, AllowedRoot: dir, Resume: true,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !res.Resumed {
		t.Fatal("result did not report resume")
	}
	if _, err := os.Stat(out + ".checkpoint.json"); !os.IsNotExist(err) {
		t.Fatalf("checkpoint should be removed after completion, err=%v", err)
	}
	reIdx, err := scan.BuildIndex(out, scan.DefaultParams())
	if err != nil {
		t.Fatal(err)
	}
	if len(reIdx.Samples) != len(samples) {
		t.Fatalf("resumed output samples=%d want %d", len(reIdx.Samples), len(samples))
	}
}

func TestWriteMP4RejectsPathEscape(t *testing.T) {
	dir := t.TempDir()
	_, err := remux.WriteMP4(testIndex(), nil, testSamples(), remux.Options{
		OutputPath: filepath.Join(dir, "..", "escape.mp4"), AllowedRoot: dir,
	})
	if !fault.IsCode(err, fault.InvalidArgument) {
		t.Fatalf("err=%v want invalid_argument", err)
	}
}
