package fault

import (
	"errors"
	"testing"
)

func TestErrorCodeWrappingAndRecord(t *testing.T) {
	inner := New(BudgetExceeded, "workers %d", 99)
	wrapped := Wrap(InvalidArgument, inner) // existing *Error preserves its code
	if !IsCode(wrapped, BudgetExceeded) {
		t.Fatalf("expected preserved code, got %v", wrapped)
	}
	var fe *Error
	if !errors.As(wrapped, &fe) || fe.Code != BudgetExceeded {
		t.Fatalf("errors.As failed: %#v", wrapped)
	}
	rec := FromError(At(EBMLBadBlock, 42, "bad block"))
	if rec.Code != EBMLBadBlock || rec.Offset != 42 {
		t.Fatalf("bad record: %+v", rec)
	}
	if IsCode(errors.New("plain"), BudgetExceeded) {
		t.Fatal("plain error unexpectedly matched code")
	}
}
