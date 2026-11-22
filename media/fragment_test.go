package media

import (
	"testing"

	"mediadist/timeline"
)

func TestBuildAndVerifyFragment(t *testing.T) {
	spec := FragmentSpec{
		TrackID: 1, SequenceNumber: 7, BaseDecodeTime: 96000,
		Samples: []FragmentSample{
			{Duration: 1000, Payload: []byte("sample-zero")},
			{Duration: 1000, Payload: []byte("sample-one")},
			{Duration: 1000, Payload: []byte("sample-two")},
		},
	}
	frag, err := BuildFragment(spec)
	if err != nil {
		t.Fatalf("BuildFragment: %v", err)
	}
	if frag.SampleCount != 3 || len(frag.SHA256) != 64 || frag.Size != len(frag.Data) {
		t.Fatalf("fragment metadata wrong: %+v", frag)
	}
	boxes, err := VerifyFragment(frag.Data)
	if err != nil {
		t.Fatalf("VerifyFragment: %v", err)
	}
	if boxes["samples"] != 3 || boxes["moof"] != 1 || boxes["mdat"] != 1 {
		t.Fatalf("unexpected structure: %v", boxes)
	}

	// Deterministic serialization: identical specs hash identically.
	again, err := BuildFragment(spec)
	if err != nil || again.SHA256 != frag.SHA256 {
		t.Fatalf("fragment hashing not deterministic: %v", err)
	}

	// Corruption is detected rather than silently accepted.
	corrupt := append([]byte(nil), frag.Data...)
	corrupt[len(corrupt)-1] ^= 0xFF
	if _, err := VerifyFragment(corrupt); err != nil && timeline.CodeOf(err) != "FRAGMENT_CORRUPT" {
		t.Fatalf("corruption code = %q", timeline.CodeOf(err))
	}
}

func TestBuildFragmentRejectsInvalid(t *testing.T) {
	if _, err := BuildFragment(FragmentSpec{TrackID: 1, SequenceNumber: 1}); timeline.CodeOf(err) != "FRAGMENT_INVALID" {
		t.Fatalf("empty samples code = %q", timeline.CodeOf(err))
	}
	bad := FragmentSpec{TrackID: 1, Samples: []FragmentSample{{Duration: 0, Payload: []byte("x")}}}
	if _, err := BuildFragment(bad); timeline.CodeOf(err) != "FRAGMENT_INVALID" {
		t.Fatalf("zero-duration sample code = %q", timeline.CodeOf(err))
	}
}
