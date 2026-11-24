package engine

import (
	"testing"

	"mediasync/internal/model"
)

func TestEnqueueReleasesContiguousPrefix(t *testing.T) {
	e, err := New(Options{WorkDir: t.TempDir(), WrapBits: 16, DriftPPM: 200, ReorderPackets: 3}, testManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(ext int64, tm model.Nanos) accepted {
		return accepted{TrackID: 1, Ext: ext, Raw: tm, Arrival: tm}
	}
	if got := e.enqueue(mk(1, 1_000_000), 0); len(got) != 1 {
		t.Fatalf("first packet released %d, want 1", len(got))
	}
	if got := e.enqueue(mk(3, 3_000_000), 0); len(got) != 0 {
		t.Fatalf("hole should hold the packet, released %d", len(got))
	}
	got := e.enqueue(mk(2, 2_000_000), 0)
	if len(got) != 2 {
		t.Fatalf("filling the hole should release 2, got %d", len(got))
	}
	if got[0].Ext != 2 || got[1].Ext != 3 {
		t.Errorf("release order = %d,%d", got[0].Ext, got[1].Ext)
	}
}

func TestEnqueueForcesOutAfterReorderWindow(t *testing.T) {
	e, err := New(Options{WorkDir: t.TempDir(), WrapBits: 16, DriftPPM: 200, ReorderPackets: 2}, testManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(ext int64) accepted { return accepted{TrackID: 1, Ext: ext, Raw: model.Nanos(ext)} }
	e.enqueue(mk(1), 0)
	e.enqueue(mk(3), 0)
	if got := e.enqueue(mk(4), 0); len(got) != 0 {
		t.Fatalf("still inside the window, released %d", len(got))
	}
	got := e.enqueue(mk(5), 0)
	if len(got) != 2 {
		t.Fatalf("window expiry should release the held run, got %d", len(got))
	}
	if got[0].Ext != 3 || got[1].Ext != 4 {
		t.Errorf("forced release order = %d,%d", got[0].Ext, got[1].Ext)
	}
}

func TestEnqueueBoundedByMaxPackets(t *testing.T) {
	e, err := New(Options{WorkDir: t.TempDir(), WrapBits: 16, DriftPPM: 200, ReorderPackets: 100, MaxPackets: 2}, testManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(ext int64) accepted { return accepted{TrackID: 1, Ext: ext, Raw: model.Nanos(ext)} }
	e.enqueue(mk(2), 0)
	e.enqueue(mk(3), 0)
	e.enqueue(mk(4), 0)
	if e.dropped == 0 {
		t.Error("exceeding the queue bound should be counted")
	}
	if e.pendingCount() > 2 {
		t.Errorf("pending = %d exceeds MaxPackets", e.pendingCount())
	}
}

func TestFlushQueuesReleasesEverything(t *testing.T) {
	e, err := New(Options{WorkDir: t.TempDir(), WrapBits: 16, DriftPPM: 200, ReorderPackets: 100}, testManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(ext int64) accepted { return accepted{TrackID: 1, Ext: ext, Raw: model.Nanos(ext)} }
	e.enqueue(mk(2), 0)
	e.enqueue(mk(5), 0)
	out := e.flushQueues()
	if len(out) != 2 {
		t.Fatalf("flushed %d, want 2", len(out))
	}
	if e.pendingCount() != 0 {
		t.Errorf("queue not empty after flush")
	}
}
