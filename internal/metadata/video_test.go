package metadata

import (
	"testing"

	"github.com/example/tsrepair/internal/model"
)

func TestColorInputWinsAndConflictFlagged(t *testing.T) {
	n := NewColorNormalizer(ColorConfig{
		DefaultPrimaries: 1, DefaultTransfer: 1, DefaultMatrix: 1})
	in := ColorMetadata{PID: 0x100, ColorPrimaries: 9, TransferCharacteristics: 16, MatrixCoefficients: 9}
	ref := ColorMetadata{PID: 0x100, ColorPrimaries: 1, TransferCharacteristics: 16, MatrixCoefficients: 9}
	res := n.Normalize(in, ref, 188)
	if res.ColorPrimaries != 9 {
		t.Fatal("input-track value must win a conflict")
	}
	if !res.Uncertain || res.Source != "preserved" {
		t.Fatal("undecidable conflict must preserve input and flag uncertainty")
	}
	if len(n.Conflicts()) != 1 || n.Conflicts()[0].Field != "color_primaries" {
		t.Fatalf("conflict record wrong: %+v", n.Conflicts())
	}
}

func TestColorMissingFilledFromConfigAndCounted(t *testing.T) {
	n := NewColorNormalizer(ColorConfig{
		DefaultPrimaries: 1, DefaultTransfer: 1, DefaultMatrix: 1})
	in := ColorMetadata{PID: 0x100, ColorPrimaries: 9}
	res := n.Normalize(in, ColorMetadata{PID: 0x100}, 0)
	if res.TransferCharacteristics != 1 || res.MatrixCoefficients != 1 {
		t.Fatal("missing fields must fall back to config defaults")
	}
	if n.MissingCount() != 2 {
		t.Fatalf("missing count=%d want 2", n.MissingCount())
	}
	if res.Uncertain {
		t.Fatal("default fill is not uncertain")
	}
}

func TestColorReferenceUsedWhenInputAbsent(t *testing.T) {
	n := NewColorNormalizer(ColorConfig{})
	res := n.Normalize(
		ColorMetadata{PID: 0x100},
		ColorMetadata{PID: 0x100, ColorPrimaries: 5, TransferCharacteristics: 6, MatrixCoefficients: 7}, 0)
	if res.ColorPrimaries != 5 || res.Source != "config" {
		t.Fatalf("reference metadata should fill absent input: %+v", res)
	}
}

func TestUndecidableErrorShape(t *testing.T) {
	err := UndecidableError(0x100, 940, "matrix")
	if err.PID != 0x100 || err.Offset != 940 || err.Stage != model.StageMetadata {
		t.Fatalf("error shape wrong: %v", err)
	}
}
