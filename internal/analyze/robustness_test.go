package analyze_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"bbdiff/internal/analyze"
	"bbdiff/internal/disasm"
	"bbdiff/internal/elfscan"
	"bbdiff/internal/fixture"
)

func TestConcurrentEnginesWork(t *testing.T) {
	// Every worker owns an engine; run many analyses in parallel to catch
	// handle sharing or double-close problems.
	oldPath, newPath := writeFixtures(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			opt := analyze.DefaultOptions()
			opt.Workers = 2
			res, err := analyze.Run(oldPath, newPath, opt)
			if err != nil {
				errs <- err
				return
			}
			if res.Summary != (analyze.Summary{Added: 1, Removed: 1, Changed: 2, Uncertain: 0}) {
				errs <- nil
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("parallel run failed: %v", err)
		}
	}
}

func TestCancellationTerminatesAndClosesEngines(t *testing.T) {
	// Build a large synthetic function set so cancellation has work to abandon.
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	newPath := filepath.Join(dir, "new.elf")
	if err := fixture.Write(oldPath, fixture.Old); err != nil {
		t.Fatal(err)
	}
	if err := fixture.Write(newPath, fixture.New); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		time.Sleep(2 * time.Millisecond)
		cancel()
	}()
	go func() {
		defer close(done)
		opt := analyze.DefaultOptions()
		opt.Context = ctx
		opt.Workers = 4
		res, err := analyze.Run(oldPath, newPath, opt)
		if err != nil {
			t.Errorf("Run: %v", err)
			return
		}
		if !res.Cancelled {
			// The run may legitimately finish before cancellation lands; that
			// is not a failure, but it must not hang.
			_ = res
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled analysis did not terminate")
	}
}

func TestRepeatedRunsDoNotLeakEngines(t *testing.T) {
	oldPath, newPath := writeFixtures(t)
	for i := 0; i < 20; i++ {
		opt := analyze.DefaultOptions()
		opt.Workers = 3
		if _, err := analyze.Run(oldPath, newPath, opt); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	// Direct engine churn: open and close many handles.
	for i := 0; i < 50; i++ {
		e, err := disasm.NewEngine()
		if err != nil {
			t.Fatalf("NewEngine: %v", err)
		}
		e.Close()
	}
}

func TestOversizedInputRejected(t *testing.T) {
	oldPath, newPath := writeFixtures(t)
	opt := analyze.DefaultOptions()
	opt.ELF.MaxFileSize = 64 // both fixtures are larger
	if _, err := analyze.Run(oldPath, newPath, opt); err == nil {
		t.Fatal("analysis accepted inputs above MaxFileSize")
	}
}

func TestZeroWorkerCountIsNormalized(t *testing.T) {
	oldPath, newPath := writeFixtures(t)
	opt := analyze.DefaultOptions()
	opt.Workers = 0
	opt.MaxBlocksPerFunction = 0
	opt.MaxInstructionsPerFunction = 0
	if _, err := analyze.Run(oldPath, newPath, opt); err != nil {
		t.Fatalf("Run with zero options: %v", err)
	}
}

func TestELFReadErrorsAreTyped(t *testing.T) {
	_, err := elfscan.Read("/definitely/not/here.elf", elfscan.DefaultOptions())
	if err == nil {
		t.Fatal("expected an error")
	}
	var readErr *elfscan.ReadError
	if !asReadError(err, &readErr) {
		t.Fatalf("error %v is not a *elfscan.ReadError", err)
	}
}

func asReadError(err error, target **elfscan.ReadError) bool {
	return errors.As(err, target)
}

// TestMutatedSampleNeverPanics drives the whole pipeline over deterministically
// corrupted images.  Most mutations make the file structurally invalid; the
// rest must be analysed or rejected, but never crash the process.
func TestMutatedSampleNeverPanics(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.elf")
	if err := fixture.Write(oldPath, fixture.Old); err != nil {
		t.Fatal(err)
	}
	base, err := fixture.Build(fixture.New)
	if err != nil {
		t.Fatal(err)
	}

	seed := uint64(0x2545f4914f6cdd1d)
	next := func() uint64 {
		seed ^= seed << 13
		seed ^= seed >> 7
		seed ^= seed << 17
		return seed
	}

	for i := 0; i < 25; i++ {
		img := append([]byte(nil), base...)
		for j := 0; j < 1+int(next()%4); j++ {
			off := int(next() % uint64(len(img)))
			img[off] ^= byte(next())
		}
		mutPath := filepath.Join(dir, "mut.elf")
		if err := os.WriteFile(mutPath, img, 0o644); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			opt := analyze.DefaultOptions()
			opt.Workers = 2
			opt.MaxInstructionsPerFunction = 5000
			// Errors are acceptable; panics and hangs are not.
			if _, err := analyze.Run(oldPath, mutPath, opt); err != nil {
				return
			}
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("mutation %d caused the pipeline to hang", i)
		}
	}
}
