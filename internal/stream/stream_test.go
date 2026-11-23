package stream

import (
	"testing"
	"time"
)

func TestQueueDropPolicyBounds(t *testing.T) {
	q, err := NewQueue(3, PolicyDrop)
	if err != nil {
		t.Fatal(err)
	}
	// Fill to capacity with non-key packets.
	for i := 0; i < 3; i++ {
		if err := q.Enqueue(Envelope{Seq: uint16(i), Key: false}); err != nil {
			t.Fatal(err)
		}
	}
	// Overflow non-key packets are dropped, never buffered.
	for i := 3; i < 100; i++ {
		if err := q.Enqueue(Envelope{Seq: uint16(i), Key: false}); err != nil {
			t.Fatal(err)
		}
	}
	if q.Len() != 3 || q.Cap() != 3 {
		t.Fatalf("queue must stay bounded: len=%d cap=%d", q.Len(), q.Cap())
	}
	if q.Dropped != 97 {
		t.Fatalf("dropped = %d, want 97", q.Dropped)
	}
}

func TestQueueKeyPacketBlocksUnderDrop(t *testing.T) {
	q, _ := NewQueue(2, PolicyDrop)
	_ = q.Enqueue(Envelope{Seq: 1, Key: true})
	_ = q.Enqueue(Envelope{Seq: 2, Key: false})
	done := make(chan error, 1)
	go func() { done <- q.Enqueue(Envelope{Seq: 3, Key: true}) }()
	select {
	case <-done:
		t.Fatal("key packet must block while the drop-policy queue is full")
	case <-time.After(30 * time.Millisecond):
	}
	if _, ok := q.Dequeue(); !ok {
		t.Fatal("dequeue failed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("key packet enqueue did not unblock after drain")
	}
}

func TestQueueBlockPolicyBackpressure(t *testing.T) {
	q, _ := NewQueue(1, PolicyBlock)
	_ = q.Enqueue(Envelope{Seq: 1})
	done := make(chan struct{})
	go func() { _ = q.Enqueue(Envelope{Seq: 2}); close(done) }()
	select {
	case <-done:
		t.Fatal("block policy must apply backpressure when full")
	case <-time.After(30 * time.Millisecond):
	}
	q.Dequeue()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked enqueue never resumed")
	}
	q.Close()
	if _, ok := q.TryDequeue(); !ok {
		t.Fatal("buffered packet must remain drainable after close")
	}
}

func TestFanoutSlowConsumerIsolation(t *testing.T) {
	f, err := NewFanout([]LayerConfig{
		{Name: "a", TargetBPS: 450000, QueueCap: 64, Policy: PolicyDrop},
		{Name: "b", TargetBPS: 900000, QueueCap: 4, Policy: PolicyDrop},
		{Name: "c", TargetBPS: 1800000, QueueCap: 64, Policy: PolicyDrop},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// Flood all layers without consumers; non-key overflow is dropped and
	// every queue stays bounded. Layer b fills first and drops the most.
	for i := 0; i < 200; i++ {
		for l := 0; l < 3; l++ {
			if err := f.Publish(Envelope{Layer: l, Seq: uint16(i), Key: false}); err != nil {
				t.Fatal(err)
			}
		}
	}
	stats := f.Stats()
	for _, s := range stats {
		if s.Len > s.Cap {
			t.Fatalf("layer %s exceeded capacity: %d > %d", s.Name, s.Len, s.Cap)
		}
	}
	if stats[1].Dropped == 0 {
		t.Fatal("small slow layer must record drops")
	}
	if stats[1].Len != stats[1].Cap {
		t.Fatalf("slow layer should be full: len=%d cap=%d", stats[1].Len, stats[1].Cap)
	}
	if f.TotalDropped() != stats[0].Dropped+stats[1].Dropped+stats[2].Dropped {
		t.Fatal("total dropped must be the per-layer sum")
	}
}

func TestFanoutSequenceTracking(t *testing.T) {
	f, _ := NewFanout([]LayerConfig{{Name: "x", QueueCap: 32, Policy: PolicyDrop}})
	defer f.Close()
	for _, seq := range []uint16{10, 11, 11, 9, 14} { // dup, reorder, then a gap of 2
		if err := f.Publish(Envelope{Layer: 0, Seq: seq, Key: false}); err != nil {
			t.Fatal(err)
		}
	}
	s := f.Stats()[0]
	if s.Duplicates != 1 || s.Reordered != 1 || s.Lost != 2 {
		t.Fatalf("tracking wrong: dup=%d reorder=%d lost=%d", s.Duplicates, s.Reordered, s.Lost)
	}
}

func TestRecovererBoundaryRecovery(t *testing.T) {
	bounds := []Boundary{
		{Index: 0, Start: 0, End: 100},
		{Index: 1, Start: 100, End: 200},
		{Index: 2, Start: 200, End: 300},
	}
	r := NewRecoverer(3, bounds, 1000) // 1000 ticks/sec => 1 tick ~ 1 ms
	// Layers 0 and 2 complete boundary 0; layer 1 gets damaged mid-way.
	if err := r.RecordDelivery(0, 100); err != nil {
		t.Fatal(err)
	}
	if err := r.RecordDelivery(2, 100); err != nil {
		t.Fatal(err)
	}
	r.MarkDamaged(1)
	if err := r.RecordDelivery(1, 100); err == nil {
		t.Fatal("damaged layer must reject delivery")
	}
	// Healthy layers keep advancing while layer 1 is isolated.
	if err := r.RecordDelivery(0, 200); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Recover(1); err != nil {
		t.Fatal(err)
	}
	states := r.RecovererStats()
	if states[1].Damaged || states[1].Recoveries != 1 {
		t.Fatalf("layer 1 not recovered: %+v", states[1])
	}
	// Layer 1 confirmed nothing yet, so it resumes at boundary 0.
	if states[1].Awaiting != 0 {
		t.Fatalf("recovery must resume at last confirmed boundary, got awaiting=%d", states[1].Awaiting)
	}
	if r.TotalRecovered() != 1 {
		t.Fatal("exactly one layer should show a recovery")
	}
}

func TestRecovererAlignmentError(t *testing.T) {
	bounds := []Boundary{{Index: 0, Start: 0, End: 1000}}
	r := NewRecoverer(1, bounds, 1000) // 1 tick = 1 ms
	// Packet lands 15 ticks after the ideal edge => 15 ms error.
	if err := r.RecordDelivery(0, 1015); err != nil {
		t.Fatal(err)
	}
	if got := r.MaxAlignErrorMS(); got != 15 {
		t.Fatalf("alignment error = %d ms, want 15", got)
	}
}

func TestFanoutDamagedLayerRejectsThenRecovers(t *testing.T) {
	f, _ := NewFanout([]LayerConfig{
		{Name: "a", QueueCap: 8, Policy: PolicyDrop},
		{Name: "b", QueueCap: 8, Policy: PolicyDrop},
	})
	defer f.Close()
	f.MarkDamaged(1)
	if err := f.Publish(Envelope{Layer: 1, Seq: 1}); err == nil {
		t.Fatal("damaged layer must reject publish")
	}
	// Layer 0 is untouched.
	if err := f.Publish(Envelope{Layer: 0, Seq: 1}); err != nil {
		t.Fatalf("healthy layer must still accept: %v", err)
	}
	f.Recover(1)
	if err := f.Publish(Envelope{Layer: 1, Seq: 2}); err != nil {
		t.Fatalf("recovered layer must accept again: %v", err)
	}
	if f.Stats()[1].Recovered != 1 {
		t.Fatal("recovery must be counted on the layer")
	}
}

func TestFanoutBlockLayerSlowConsumerIsolation(t *testing.T) {
	f, _ := NewFanout([]LayerConfig{
		{Name: "slow", QueueCap: 2, Policy: PolicyBlock},
		{Name: "fast", QueueCap: 2, Policy: PolicyBlock},
	})
	defer f.Close()
	// Drain the fast layer eagerly; never drain the slow one past capacity.
	go func() { _ = f.Drain(1, func(Envelope) {}) }()
	fastDone := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			if err := f.Publish(Envelope{Layer: 1, Seq: uint16(i), Key: false}); err != nil {
				return
			}
		}
		close(fastDone)
	}()
	select {
	case <-fastDone:
	case <-time.After(5 * time.Second):
		t.Fatal("fast layer stalled behind the blocked slow layer")
	}
}
