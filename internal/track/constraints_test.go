package track

import (
	"testing"

	"mediacheck/internal/clock"
	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
)

var allCodecs = []codec.Kind{codec.H264, codec.H265, codec.AV1}

func goodMappings() []Mapping {
	return []Mapping{
		{TrackID: 1, Kind: codec.H264, SrcIndex: 0},
		{TrackID: 2, Kind: codec.H265, SrcIndex: 1},
		{TrackID: 3, Kind: codec.AV1, SrcIndex: 2},
	}
}

func TestValidateMappingsOK(t *testing.T) {
	if err := ValidateMappings(goodMappings(), allCodecs, 8); err != nil {
		t.Fatal(err)
	}
}

func TestValidateMappingsMissing(t *testing.T) {
	ms := goodMappings()[:2]
	err := ValidateMappings(ms, allCodecs, 8)
	if !fault.HasCode(err, fault.ErrMissingTrack) {
		t.Fatalf("got %v", err)
	}
}

func TestValidateMappingsDuplicate(t *testing.T) {
	ms := goodMappings()
	ms[1].TrackID = 1 // collide with first
	if err := ValidateMappings(ms, allCodecs, 8); !fault.HasCode(err, fault.ErrDuplicateMap) {
		t.Fatalf("track id: %v", err)
	}
	ms = goodMappings()
	ms[2].SrcIndex = 0 // same source stream twice
	if err := ValidateMappings(ms, allCodecs, 8); !fault.HasCode(err, fault.ErrDuplicateMap) {
		t.Fatalf("src index: %v", err)
	}
}

func TestValidateMappingsLimit(t *testing.T) {
	err := ValidateMappings(goodMappings(), allCodecs, 2)
	if !fault.HasCode(err, fault.ErrLimitExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestCheckMonotonic(t *testing.T) {
	if err := CheckMonotonic([]clock.Ticks{0, 0, 3000, 6000}); err != nil {
		t.Fatal(err)
	}
	err := CheckMonotonic([]clock.Ticks{0, 6000, 3000})
	fe, ok := err.(*fault.Error)
	if !ok || fe.Code != fault.ErrTimeRegression || fe.Offset != 2 {
		t.Fatalf("got %v", err)
	}
}
