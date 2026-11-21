package pipeline

import (
	"errors"
	"testing"

	"bitrack/internal/model"
)

func TestNoopInjectorNeverFires(t *testing.T) {
	n := NoopInjector{}
	seg := &model.Segment{Index: 0}
	if err := n.OnEncode(seg); err != nil {
		t.Errorf("noop encode: %v", err)
	}
	if err := n.OnValidate(seg); err != nil {
		t.Errorf("noop validate: %v", err)
	}
}

func TestFaultInjectorFiresOnce(t *testing.T) {
	dir := t.TempDir()
	f := NewFaultInjector(FaultFFmpegFail, -1, dir)
	seg := &model.Segment{Index: 7}
	err := f.OnEncode(seg)
	if err == nil {
		t.Fatal("first encode must fault")
	}
	if !IsPermanent(err) {
		t.Error("injected fault must be permanent")
	}
	if !f.Fired() {
		t.Error("Fired() should be true after triggering")
	}
	// One-shot: subsequent calls, including other segments, must pass through.
	if err := f.OnEncode(&model.Segment{Index: 8}); err != nil {
		t.Errorf("second call must not fault, got %v", err)
	}
}

func TestFaultInjectorTargetSegment(t *testing.T) {
	dir := t.TempDir()
	f := NewFaultInjector(FaultFFmpegFail, 3, dir)
	if err := f.OnEncode(&model.Segment{Index: 2}); err != nil {
		t.Errorf("non-target segment must not fault, got %v", err)
	}
	if err := f.OnEncode(&model.Segment{Index: 3}); err == nil || !IsPermanent(err) {
		t.Errorf("target segment must fault permanently, got %v", err)
	}
}

func TestDiskFailInjectorBlocksOutputDir(t *testing.T) {
	dir := t.TempDir()
	f := NewFaultInjector(FaultDiskFail, -1, dir)
	err := f.OnEncode(&model.Segment{Index: 0})
	if err == nil || !IsPermanent(err) {
		t.Fatalf("disk fault must be a permanent error, got %v", err)
	}
}

func TestPermanentErrorUnwrap(t *testing.T) {
	base := errors.New("root cause")
	pe := &PermanentError{Err: base}
	if !errors.Is(pe, base) {
		t.Error("Unwrap should expose the wrapped cause")
	}
	if !IsPermanent(pe) {
		t.Error("IsPermanent should recognize the wrapper")
	}
	if IsPermanent(base) {
		t.Error("a plain error is not permanent")
	}
}

func TestBaselineCorruptor(t *testing.T) {
	path := t.TempDir() + "/baseline.json"
	if err := BaselineCorruptor(path); err != nil {
		t.Fatal(err)
	}
	if err := BaselineCorruptor(path); err != nil {
		t.Fatalf("corruptor must be repeatable: %v", err)
	}
}
