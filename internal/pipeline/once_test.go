// This file exercises invariants that need direct access to the simulator
// internals rather than a full fixture run.

package pipeline

import (
	"testing"

	"mediad/internal/buffer"
	"mediad/internal/model"
	"mediad/internal/recovery"
	"mediad/internal/rtp"
	"mediad/internal/sync"
	"mediad/internal/timeline"
)

func newTestSim() *sim {
	depth := int64(100)
	s := &sim{
		ext: map[uint32]*rtp.Extender{}, mediaOf: map[uint32]model.MediaType{},
		jit: buffer.NewJitter(depth), wm: timeline.NewWatermark(depth, 250),
		replay: timeline.NewReplay(), rec: recovery.NewRecoverer(recovery.NewCache(64)),
		est: sync.NewEstimator(), depth: depth,
		next: map[uint32]uint64{}, nextSet: map[uint32]bool{},
		lastExt: map[uint32]uint64{}, lastEvMs: map[uint32]int64{}, stepMs: map[uint32]int64{},
		missing: map[seqKey]*lossInfo{}, accepted: map[seqKey]bool{}, emitted: map[seqKey]bool{},
	}
	s.sum.MalformedByClass = map[string]int{}
	s.sum.QuarantineReasons = map[string]int{}
	return s
}

func TestRecoveredThenOriginalEmitsOnce(t *testing.T) {
	// One extended sequence number must produce exactly one media sample:
	// a recovery followed by the delayed original counts as a duplicate.
	s := newTestSim()
	s.wm.Advance(650)
	pkt := &model.Event{Kind: model.KindPacket, Media: model.MediaVideo, SSRC: 7, Seq16: 10, RTPTs: 0, EventMs: 500, ArrivalMs: 500, Size: 10}
	s.rec.Offer(pkt)
	// Establish the stream baseline and the gap.
	base := &model.Event{Kind: model.KindPacket, Media: model.MediaVideo, SSRC: 7, Seq16: 9, RTPTs: 0, EventMs: 400, ArrivalMs: 400, Size: 10}
	s.onPacket(base)
	later := &model.Event{Kind: model.KindPacket, Media: model.MediaVideo, SSRC: 7, Seq16: 11, RTPTs: 0, EventMs: 600, ArrivalMs: 600, Size: 10}
	s.onPacket(later)
	if len(s.missing) == 0 {
		t.Fatal("gap not recorded")
	}
	nack := &model.Event{Kind: model.KindNACK, SSRC: 7, NACKSeqs: []uint16{10}, ArrivalMs: 620}
	s.onNACK(nack)
	if s.rec.Stats.Recovered != 1 {
		t.Fatalf("recovered = %d", s.rec.Stats.Recovered)
	}
	// The original now arrives; it must not emit a second sample.
	orig := &model.Event{Kind: model.KindPacket, Media: model.MediaVideo, SSRC: 7, Seq16: 10, RTPTs: 0, EventMs: 500, ArrivalMs: 700, Size: 10}
	s.onPacket(orig)
	if s.sum.DuplicatePackets != 1 {
		t.Fatalf("duplicate count = %d, want 1", s.sum.DuplicatePackets)
	}
	video := 0
	for _, ev := range s.replay.Outputs() {
		if ev.Seq == 10 {
			video++
		}
	}
	if video != 1 {
		t.Fatalf("seq 10 appears %d times, want 1", video)
	}
}
