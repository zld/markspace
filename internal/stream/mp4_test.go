package stream

import (
	"os"
	"path/filepath"
	"testing"

	"mediaarchive/internal/diag"
)

func TestMP4MissingStblIsStructuralFault(t *testing.T) {
	// ftyp + empty moov: no tracks at all. The analyzer must still succeed
	// and report a structure fault rather than panicking.
	data := []byte{
		0, 0, 0, 16, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 2, 0,
		0, 0, 0, 8, 'm', 'o', 'o', 'v',
	}
	p := filepath.Join(t.TempDir(), "empty.mp4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := AnalyzeMP4(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Tracks) != 0 || a.Video != nil {
		t.Fatalf("tracks = %v", a.Tracks)
	}
}

func TestMP4NotAnMP4(t *testing.T) {
	p := filepath.Join(t.TempDir(), "garbage.mp4")
	if err := os.WriteFile(p, []byte("this is not a box structure at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := AnalyzeMP4(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Faults) == 0 {
		t.Fatal("garbage accepted without fault")
	}
}

func TestMP4TruncatedStblFaultOffset(t *testing.T) {
	data := minimalMP4(t)
	// Cut the moov in half so the sample table itself is incomplete.
	cut := data[:len(data)/2]
	p := filepath.Join(t.TempDir(), "half.mp4")
	if err := os.WriteFile(p, cut, 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := AnalyzeMP4(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Faults) == 0 {
		t.Fatal("half file accepted cleanly")
	}
	f, ok := diag.As(a.Faults[0])
	if !ok || f.Offset == 0 {
		t.Fatalf("fault lacks an offset: %+v", a.Faults[0])
	}
}

func TestExpandSamplesValidatesTables(t *testing.T) {
	tr := &Track{ID: 1, Timescale: 1000}
	samples, faults := expandSamples(tr, sampleTables{
		stts:  []sttsEntry{{SampleCount: 4, SampleDelta: 40}},
		sizes: []uint32{10, 10},
	})
	if len(samples) != 2 {
		t.Fatalf("samples = %d", len(samples))
	}
	found := false
	for _, f := range faults {
		if f.Code == diag.CodeMP4Structure {
			found = true
		}
	}
	if !found {
		t.Fatalf("mismatch not reported: %v", faults)
	}
}

func TestNoStssMeansAllSync(t *testing.T) {
	tr := &Track{ID: 1, Timescale: 1000}
	samples, _ := expandSamples(tr, sampleTables{
		stts:  []sttsEntry{{SampleCount: 3, SampleDelta: 40}},
		sizes: []uint32{1, 1, 1},
	})
	for _, s := range samples {
		if !s.Sync {
			t.Fatalf("sample %d not sync without stss", s.Index)
		}
	}
}
