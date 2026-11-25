package stream

import (
	"sync"
	"testing"
	"time"

	"packcheck/internal/segment"
)

func TestBoundedQueuePeak(t *testing.T) {
	f := NewFanOut(2, time.Second, "low", "mid", "high")
	defer f.CloseAll()
	// Every layer has a live consumer, so back-pressure only ever manifests
	// as a queue-depth high-water mark, never as isolation here.
	var wg sync.WaitGroup
	for _, l := range f.Layers() {
		wg.Add(1)
		go func(layer *Layer) {
			defer wg.Done()
			for range layer.Drain() {
			}
		}(l)
	}
	for i := 0; i < 6; i++ {
		f.Send(Item{Sequence: i, Timestamp: uint32(i) * 3600, ByteOffset: int64(i)})
	}
	f.CloseAll()
	wg.Wait()
	for _, l := range f.Layers() {
		if l.Peak() > 2 {
			t.Errorf("layer %s peak=%d exceeds capacity 2", l.Name(), l.Peak())
		}
		if l.Isolated() {
			t.Errorf("layer %s must not be isolated with an active consumer", l.Name())
		}
	}
}

func TestSlowConsumerIsolationDoesNotBlockOthers(t *testing.T) {
	// The "slow" layer never drains; with capacity 2 its third Send times
	// out and isolates it. Fast layers keep receiving every item.
	f := NewFanOut(2, 20*time.Millisecond, "fast", "slow")
	defer f.CloseAll()
	fastDone := make(chan Item, 16)
	go func() {
		for it := range f.Layer("fast").Drain() {
			fastDone <- it
		}
		close(fastDone)
	}()
	for i := 0; i < 6; i++ {
		f.Send(Item{Sequence: i, Timestamp: uint32(i) * 3600, ByteOffset: int64(i * 10)})
	}
	slow := f.Layer("slow")
	if !slow.Isolated() {
		t.Fatal("slow layer must be isolated")
	}
	if f.Issues().CountByCode()["slow-consumer"] == 0 {
		t.Error("slow-consumer issue not recorded")
	}
	dropped := f.Dropped()
	if len(dropped) != 1 || dropped[0].Representation != "slow" ||
		dropped[0].StartOffset >= dropped[0].EndOffset {
		t.Errorf("dropped interval wrong: %+v", dropped)
	}
	// The fast layer still got all six items; isolation did not block it.
	got := 0
loop:
	for {
		select {
		case <-fastDone:
			got++
		case <-time.After(time.Second):
			break loop
		}
	}
	if got != 6 {
		t.Errorf("fast layer got %d items, want 6", got)
	}
}

func TestFanInConvergenceAndSignatures(t *testing.T) {
	pktsA, err := segment.BuildPackets(10, 0, 3600, 1)
	if err != nil {
		t.Fatal(err)
	}
	pktsB, _ := segment.BuildPackets(10, 0, 3600, 2)
	fi := NewFanIn()
	fi.Submit(LayerOutput{Representation: "low", Packets: pktsA, Healthy: true})
	fi.Submit(LayerOutput{Representation: "mid", Packets: pktsB, Healthy: true})
	fi.Submit(LayerOutput{Representation: "high", Packets: nil, Healthy: false})

	if len(fi.HealthyLayers()) != 2 {
		t.Error("isolated layer must be excluded from healthy set")
	}
	shared := fi.SharedTimestamps()
	if len(shared) != 10 || shared[1] != 3600 {
		t.Errorf("shared clock view wrong: %v", shared)
	}
	sigs, err := fi.Signatures()
	if err != nil {
		t.Fatal(err)
	}
	if len(sigs) != 3 || sigs[2].Healthy {
		t.Errorf("signatures wrong: %+v", sigs)
	}
	aligned, _ := segment.CrossCheck(sigs)
	if !aligned {
		t.Error("healthy layers with identical timestamp grids must align")
	}
}
