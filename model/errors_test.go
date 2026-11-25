package model

import (
	"errors"
	"testing"
)

func TestErrorCategoryAndRecoverable(t *testing.T) {
	cause := errors.New("boom")
	err := NewError("track", "source:1", 42, CategoryTruncatedBox, "bad box", cause).RecoverableWarn()
	if err == nil || !err.Recoverable || !err.Warning {
		t.Fatalf("error should be recoverable warning: %+v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("wrapped cause not available through errors.Is")
	}
	if !IsCategory(err, CategoryTruncatedBox) {
		t.Fatal("category not detected")
	}
	var me *Error
	if !errors.As(err, &me) || me.Offset != 42 || me.Track != "track" {
		t.Fatalf("error metadata not preserved: %+v", me)
	}
}
