package cmaf_test

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"

	"mediapack/internal/cmaf"
	"mediapack/internal/clock"
	"mediapack/internal/fixture"
)

func build(t *testing.T, o fixture.FragOpts) []byte {
	t.Helper()
	data, err := fixture.BuildFragment(fixture.TrackDesc(1), o)
	if err != nil {
		t.Fatalf("BuildFragment: %v", err)
	}
	return data
}

func TestParseFragmentValid(t *testing.T) {
	data := build(t, fixture.FragOpts{
		Mfhd: 7, Tfdt: 9000, HasTfdt: true, Keyframe: true, Duration: 3000,
		Seed: []byte{1, 2, 3},
	})
	f, err := cmaf.ParseFragment(data)
	if err != nil {
		t.Fatalf("ParseFragment: %v", err)
	}
	if f.TrackID != 1 || f.SequenceNumber != 7 || !f.HasTfdt || f.BaseDecodeTime != 9000 {
		t.Errorf("unexpected fragment: %+v", f)
	}
	if f.Duration != 3000 || !f.Keyframe || f.MoofOffset != 0 {
		t.Errorf("unexpected fragment: %+v", f)
	}
}

func TestParseFragmentNonSync(t *testing.T) {
	data := build(t, fixture.FragOpts{Mfhd: 1, HasTfdt: true, Keyframe: false, Duration: 3000})
	f, err := cmaf.ParseFragment(data)
	if err != nil {
		t.Fatalf("ParseFragment: %v", err)
	}
	if f.Keyframe {
		t.Error("non-sync sample must not be a keyframe")
	}
}

func TestParseFragmentTruncatedHeader(t *testing.T) {
	data := build(t, fixture.FragOpts{Mfhd: 1, HasTfdt: true, Duration: 3000})
	_, err := cmaf.ParseFragment(data[:5])
	if err == nil || !clock.HasCategory(err, clock.CatParse) {
		t.Fatalf("want parse error, got %v", err)
	}
	var ce *clock.Error
	if !errors.As(err, &ce) || ce.Offset != 0 {
		t.Errorf("error must carry offset 0: %v", err)
	}
	if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("want truncated message: %v", err)
	}
}

func TestParseFragmentOverrunningBox(t *testing.T) {
	data := build(t, fixture.FragOpts{Mfhd: 1, HasTfdt: true, Duration: 3000})
	binary.BigEndian.PutUint32(data[0:4], uint32(len(data)*4)) // moof overruns
	_, err := cmaf.ParseFragment(data)
	if err == nil || !clock.HasCategory(err, clock.CatParse) {
		t.Fatalf("want parse error, got %v", err)
	}
	if !strings.Contains(err.Error(), "overruns") {
		t.Errorf("want overrun message: %v", err)
	}
}

func TestParseFragmentMissingMfhd(t *testing.T) {
	data := build(t, fixture.FragOpts{Mfhd: 1, HasTfdt: true, Duration: 3000})
	// mfhd is the first child inside moof: 8-byte moof header, then a
	// 16-byte mfhd box. Remove it and shrink the moof size.
	moofSize := int64(binary.BigEndian.Uint32(data[0:4]))
	binary.BigEndian.PutUint32(data[0:4], uint32(moofSize-16))
	stripped := append(data[:8:8], data[24:]...)
	_, err := cmaf.ParseFragment(stripped)
	if err == nil || !strings.Contains(err.Error(), "missing mfhd") {
		t.Fatalf("want missing mfhd error, got %v", err)
	}
}

func TestParseFragmentMissingTfdt(t *testing.T) {
	data := build(t, fixture.FragOpts{Mfhd: 3, HasTfdt: false, Duration: 1024})
	f, err := cmaf.ParseFragment(data)
	if err != nil {
		t.Fatalf("ParseFragment: %v", err)
	}
	if f.HasTfdt {
		t.Error("HasTfdt must be false")
	}
	if f.Duration != 1024 {
		t.Errorf("duration must still be derivable: %d", f.Duration)
	}
}

func TestParseFragmentUnderivableDuration(t *testing.T) {
	data := build(t, fixture.FragOpts{Mfhd: 3, HasTfdt: false, Duration: 0})
	f, err := cmaf.ParseFragment(data)
	if err != nil {
		t.Fatalf("ParseFragment: %v", err)
	}
	if f.Duration != 0 {
		t.Errorf("duration must be underivable: %d", f.Duration)
	}
}

func TestParseSourceMarkers(t *testing.T) {
	dir := t.TempDir()
	cfg := fixture.Config{Dir: dir, Tracks: 2, Fragments: 8}
	if err := fixture.Generate(cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(dir + "/track-0001/markers.rtp")
	if err != nil {
		t.Fatalf("read markers: %v", err)
	}
	markers, err := cmaf.ParseSourceMarkers(data)
	if err != nil {
		t.Fatalf("ParseSourceMarkers: %v", err)
	}
	if len(markers) != 4 {
		t.Fatalf("want 4 markers, got %d", len(markers))
	}
	for i, m := range markers {
		if m.SSRC != 1 || uint32(m.Sequence) != uint32(i) {
			t.Errorf("marker %d out of order: %+v", i, m)
		}
	}
}

func TestParseSourceMarkersTruncated(t *testing.T) {
	dir := t.TempDir()
	if err := fixture.Generate(fixture.Config{Dir: dir, Tracks: 1, Fragments: 2}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	data, err := os.ReadFile(dir + "/track-0001/markers.rtp")
	if err != nil {
		t.Fatalf("read markers: %v", err)
	}
	if _, err := cmaf.ParseSourceMarkers(data[:len(data)-3]); err == nil {
		t.Fatal("want truncation error")
	}
}

