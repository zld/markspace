package job

import (
	"testing"
	"time"
)

func TestStateSaveLoadAndCommit(t *testing.T) {
	root := t.TempDir()
	s, isOld, err := Load(root, "j1")
	if err != nil || isOld {
		t.Fatalf("new state old=%v err=%v", isOld, err)
	}
	s.JobKey = "key"
	s.Stage("scan-a").Status = StageCommitted
	if err := s.Save(root, "j1"); err != nil {
		t.Fatal(err)
	}
	loaded, old, err := Load(root, "j1")
	if err != nil || !old {
		t.Fatalf("load old=%v err=%v", old, err)
	}
	if loaded.JobKey != "key" || !loaded.IsCommitted("scan-a") {
		t.Fatalf("state not persisted: %#v", loaded)
	}
}

func TestNearestSyncAndResumeRange(t *testing.T) {
	sync := []int{1, 4, 9}
	if got := NearestSync(sync, 3); got != 1 {
		t.Fatalf("nearest through 3 = %d", got)
	}
	if got := NearestSync(sync, 8); got != 4 {
		t.Fatalf("nearest through 8 = %d", got)
	}
	cp, first, last := ResumeSampleRange(sync, 10, 12)
	if cp != 9 || first != 9 || last != 12 {
		t.Fatalf("range=%d,%d,%d", cp, first, last)
	}
}

func TestKeyLockSerializesAndReleases(t *testing.T) {
	root := t.TempDir()
	l1, err := AcquireKeyLock(root, "abc")
	if err != nil {
		t.Fatal(err)
	}
	if err := l1.Unlock(); err != nil {
		t.Fatal(err)
	}
	l2, err := AcquireKeyLock(root, "abc")
	if err != nil {
		t.Fatal(err)
	}
	_ = l2.Unlock()
}

func TestKeyLockMutualExclusion(t *testing.T) {
	root := t.TempDir()
	held, err := AcquireKeyLock(root, "serial")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan *Lock, 1)
	go func() {
		l, err := AcquireKeyLock(root, "serial")
		if err != nil {
			t.Errorf("second lock: %v", err)
			return
		}
		got <- l
	}()
	select {
	case l := <-got:
		_ = l.Unlock()
		t.Fatal("same job key entered critical section concurrently")
	case <-time.After(50 * time.Millisecond):
	}
	if err := held.Unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case l := <-got:
		_ = l.Unlock()
	case <-time.After(time.Second):
		t.Fatal("lock was not released")
	}
}
