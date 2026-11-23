package recovery

import (
	"testing"

	"mediad/internal/model"
)

func pkt(ssrc uint32, seq uint16) *model.Event {
	return &model.Event{Kind: model.KindPacket, SSRC: ssrc, Seq16: seq, Seq: uint64(seq)}
}

func TestCacheEvictsOldest(t *testing.T) {
	c := NewCache(2)
	c.Add(pkt(1, 10))
	c.Add(pkt(1, 11))
	c.Add(pkt(1, 12))
	if _, ok := c.Get(1, 10); ok {
		t.Fatal("oldest entry was not evicted")
	}
	if _, ok := c.Get(1, 12); !ok {
		t.Fatal("newest entry missing")
	}
	if c.Len() != 2 {
		t.Fatalf("len = %d, want 2", c.Len())
	}
}

func TestCacheKeysBySSRCAndSeq(t *testing.T) {
	c := NewCache(4)
	c.Add(pkt(1, 5))
	if _, ok := c.Get(2, 5); ok {
		t.Fatal("cache ignored the SSRC in its key")
	}
	if _, ok := c.Get(1, 5); !ok {
		t.Fatal("cache lost the packet")
	}
}

func TestOnNACKStates(t *testing.T) {
	c := NewCache(8)
	for _, s := range []uint16{1, 2, 3, 4} {
		c.Add(pkt(9, s))
	}
	r := NewRecoverer(c)
	states := map[uint64]LossState{
		1: StatePending,
		2: StateDeclared,
		3: StateFinalized,
		4: StatePresent,
		5: StatePending, // absent from cache
	}
	reqs := []Request{
		{Seq: 1, SSRC: 9, Seq16: 1},
		{Seq: 2, SSRC: 9, Seq16: 2},
		{Seq: 3, SSRC: 9, Seq16: 3},
		{Seq: 4, SSRC: 9, Seq16: 4},
		{Seq: 5, SSRC: 9, Seq16: 5},
	}
	normal, late := r.OnNACK(reqs, func(seq uint64) LossState { return states[seq] })
	if len(normal) != 1 || normal[0].Seq != 1 {
		t.Fatalf("normal = %+v", normal)
	}
	if len(late) != 1 || late[0].Seq != 2 {
		t.Fatalf("late = %+v", late)
	}
	if r.Stats.LateFeedback != 1 {
		t.Fatalf("late feedback = %d", r.Stats.LateFeedback)
	}
	if r.Stats.CacheMiss != 1 {
		t.Fatalf("cache miss = %d", r.Stats.CacheMiss)
	}
	if len(late) > 0 && late[0].Kind != model.KindRecovered {
		t.Fatalf("recovered kind = %v", late[0].Kind)
	}
}

func TestOnNACKNeverFabricates(t *testing.T) {
	r := NewRecoverer(NewCache(1))
	normal, late := r.OnNACK([]Request{{Seq: 42, SSRC: 1, Seq16: 42}}, func(uint64) LossState {
		return StatePending
	})
	if len(normal) != 0 || len(late) != 0 {
		t.Fatalf("fabricated media: %+v %+v", normal, late)
	}
	if r.Stats.CacheMiss != 1 || r.Stats.Recovered != 0 {
		t.Fatalf("stats = %+v", r.Stats)
	}
}

func TestDuplicateNACKCountedOnce(t *testing.T) {
	c := NewCache(4)
	c.Add(pkt(1, 7))
	r := NewRecoverer(c)
	req := []Request{{Seq: 7, SSRC: 1, Seq16: 7}}
	r.OnNACK(req, func(uint64) LossState { return StatePending })
	r.OnNACK(req, func(uint64) LossState { return StatePending })
	if r.Stats.DupNACK != 1 {
		t.Fatalf("dup nack = %d, want 1", r.Stats.DupNACK)
	}
	if r.Stats.Recovered != 1 {
		t.Fatalf("recovered = %d, want 1", r.Stats.Recovered)
	}
}
