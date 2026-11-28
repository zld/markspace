package container

import (
	"testing"

	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
)

func testSpecs() []TrackSpec {
	return []TrackSpec{
		{ID: 1, Kind: codec.H264, Timescale: 90000, SampleDelta: 3000,
			Samples:    [][]byte{[]byte("sps-pps-idr"), []byte("slice-a"), []byte("slice-b")},
			KeySamples: []uint32{1}},
		{ID: 2, Kind: codec.H265, Timescale: 90000, SampleDelta: 3000,
			Samples:    [][]byte{[]byte("vps-sps-pps-idr"), []byte("trail")},
			KeySamples: []uint32{1}},
		{ID: 3, Kind: codec.AV1, Timescale: 90000, SampleDelta: 3000,
			Samples:    [][]byte{[]byte("seq-td-frame"), []byte("frame2")},
			KeySamples: []uint32{1}},
	}
}

func TestBuildCheckRoundTrip(t *testing.T) {
	specs := testSpecs()
	data, err := Build(specs)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Check(data, specs)
	if err != nil {
		t.Fatal(err)
	}
	if rep.MajorBrand != "isom" {
		t.Fatalf("brand %q", rep.MajorBrand)
	}
	if len(rep.Tracks) != 3 {
		t.Fatalf("tracks %+v", rep.Tracks)
	}
	for i, tr := range rep.Tracks {
		if tr.Kind != specs[i].Kind || tr.Timescale != 90000 {
			t.Fatalf("track %d %+v", i, tr)
		}
		if tr.SampleCount != uint32(len(specs[i].Samples)) || tr.KeyCount != 1 {
			t.Fatalf("track %d %+v", i, tr)
		}
	}
}

func TestBuildDeterministic(t *testing.T) {
	a, err1 := Build(testSpecs())
	b, err2 := Build(testSpecs())
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	if string(a) != string(b) {
		t.Fatal("container bytes differ between runs")
	}
}

func TestCheckDetectsMismatch(t *testing.T) {
	specs := testSpecs()
	data, err := Build(specs)
	if err != nil {
		t.Fatal(err)
	}
	bad := testSpecs()
	bad[1].Timescale = 48000
	if _, err := Check(data, bad); !fault.HasCode(err, fault.ErrIntegrity) {
		t.Fatalf("timescale mismatch: %v", err)
	}
	bad = testSpecs()
	bad[0].Samples = bad[0].Samples[:2]
	if _, err := Check(data, bad); !fault.HasCode(err, fault.ErrIntegrity) {
		t.Fatalf("sample count mismatch: %v", err)
	}
}

func TestCheckDetectsTruncation(t *testing.T) {
	specs := testSpecs()
	data, err := Build(specs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Check(data[:len(data)/2], specs); err == nil {
		t.Fatal("truncated container must fail")
	}
}

func TestBuildRejectsEmpty(t *testing.T) {
	if _, err := Build(nil); !fault.HasCode(err, fault.ErrMissingTrack) {
		t.Fatalf("got %v", err)
	}
}
