package index

import (
	"testing"

	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
)

func testUnits() []codec.Unit {
	return []codec.Unit{
		{Offset: 4, Name: "SPS"},
		{Offset: 12, Name: "IDR", Keyframe: true},
		{Offset: 40, Name: "slice"},
		{Offset: 60, Name: "IDR", Keyframe: true},
	}
}

func TestBuildAndValidate(t *testing.T) {
	units := testUnits()
	idx := Build(units)
	if len(idx.Offsets) != 2 || idx.Offsets[0] != 12 || idx.Offsets[1] != 60 {
		t.Fatalf("idx %+v", idx)
	}
	if err := idx.Validate(units, 100); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsOutOfRange(t *testing.T) {
	units := testUnits()
	err := Index{Offsets: []int64{12, 100}}.Validate(units, 100)
	assertCode(t, err, fault.ErrIndexOutOfRange, 100)
	err = Index{Offsets: []int64{-1}}.Validate(units, 100)
	assertCode(t, err, fault.ErrIndexOutOfRange, -1)
}

func TestValidateRejectsUnsorted(t *testing.T) {
	err := Index{Offsets: []int64{60, 12}}.Validate(testUnits(), 100)
	assertCode(t, err, fault.ErrIndexOutOfRange, 12)
}

func TestValidateRejectsNonKeyframe(t *testing.T) {
	err := Index{Offsets: []int64{40}}.Validate(testUnits(), 100)
	assertCode(t, err, fault.ErrIndexOutOfRange, 40)
}

func TestValidateRejectsEmpty(t *testing.T) {
	err := Index{}.Validate(testUnits(), 100)
	assertCode(t, err, fault.ErrIndexOutOfRange, 0)
}

func assertCode(t *testing.T, err error, c fault.Code, off int64) {
	t.Helper()
	fe, ok := err.(*fault.Error)
	if !ok || fe.Code != c || fe.Offset != off {
		t.Fatalf("got %v want %s offset=%d", err, c, off)
	}
}
