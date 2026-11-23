// Package fixture deterministically generates the local media fixtures used
// to exercise every QC path. No external files are read: all RTP packets, FEC
// groups, MP4 fragments and playlists are synthesized from a seed.
package fixture

import (
	"fmt"
	"math/rand"

	"mediaqc/internal/clock"
	"mediaqc/internal/model"
	"mediaqc/internal/recovery"
)

// Kind labels the generated fault scenario.
type Kind string

const (
	KindHealthy      Kind = "healthy"
	KindReorderDup   Kind = "reorder_duplicate"
	KindSingleLoss   Kind = "single_loss"
	KindDoubleLoss   Kind = "double_loss"
	KindTruncatedMP4 Kind = "truncated_mp4"
	KindBadTimescale Kind = "bad_timescale"
	KindMissingRef   Kind = "missing_reference"
	KindColorClash   Kind = "color_clash"
)

// Material is one generated input with its expected diagnostic shape.
type Material struct {
	ID         string
	Kind       Kind
	Events     []model.Event
	Gaps       []model.Gap
	FECGroups  []recovery.Group
	// MP4Raw is non-nil for the truncation scenario.
	MP4Raw     []byte
	// HLSPlaylist is non-nil for the missing-reference scenario.
	HLSPlaylist string
	// Timescale is zero in the bad-timescale scenario to force rejection.
	Timescale int64
	// ColorDecls carries conflicting/unknown color metadata.
	ColorDecls []int
}

// Generator builds seeded, reproducible fixtures.
type Generator struct {
	rng       *rand.Rand
	seed      int64
	clockRate int64
}

// NewGenerator creates a generator from an explicit seed.
func NewGenerator(seed int64) *Generator {
	return &Generator{rng: rand.New(rand.NewSource(seed)), seed: seed, clockRate: 90000}
}

// GenerateAll builds one fixture of every fault class plus a healthy one.
func (g *Generator) GenerateAll() ([]Material, error) {
	var out []Material
	out = append(out, g.healthy())
	out = append(out, g.reorderDuplicate())
	out = append(out, g.singleLoss())
	out = append(out, g.doubleLoss())
	out = append(out, g.truncatedMP4())
	out = append(out, g.badTimescale())
	out = append(out, g.missingReference())
	out = append(out, g.colorClash())
	return out, nil
}

func (g *Generator) tb() clock.Timebase {
	tb, _ := clock.New(g.clockRate)
	return tb
}

func (g *Generator) event(id, track string, seq uint64) model.Event {
	tb := g.tb()
	ts := int64(seq) * 3000 // 30 fps at 90 kHz
	payload := []byte{byte(seq), byte(seq >> 8), g.seedByte()}
	return model.Event{
		MaterialID: id, TrackID: track, Kind: model.TrackVideo, Sequence: seq,
		Timebase: tb, Start: tb.At(ts), Duration: tb.At(3000), PTS: tb.At(ts),
		Payload: payload, Size: len(payload), Origin: fmt.Sprintf("ssrc=1 seq=%d", seq),
	}
}

func (g *Generator) seedByte() byte { return byte(g.rng.Intn(256)) }

// fecGroup builds a parity group over the given sequences of these events.
func fecGroup(groupID uint64, evs []model.Event, members []uint64) recovery.Group {
	bySeq := map[uint64]model.Event{}
	for _, ev := range evs {
		bySeq[ev.Sequence] = ev
	}
	var payloads [][]byte
	for _, m := range members {
		payloads = append(payloads, bySeq[m].Payload)
	}
	return recovery.Group{TrackID: "v", GroupID: groupID, Members: members, Parity: recovery.Parity(payloads)}
}

func (g *Generator) healthy() Material {
	evs := []model.Event{g.event("mat-healthy", "v", 0), g.event("mat-healthy", "v", 1), g.event("mat-healthy", "v", 2)}
	return Material{ID: "mat-healthy", Kind: KindHealthy, Events: evs,
		FECGroups: []recovery.Group{fecGroup(0, evs, []uint64{0, 1, 2})}, Timescale: g.clockRate}
}

func (g *Generator) reorderDuplicate() Material {
	id := "mat-reorder"
	base := []model.Event{g.event(id, "v", 0), g.event(id, "v", 1), g.event(id, "v", 2), g.event(id, "v", 3)}
	// Deliver 0, 2, 1 (reorder) then 2 again (duplicate).
	evs := []model.Event{base[0], base[2], base[1], base[2]}
	return Material{ID: id, Kind: KindReorderDup, Events: evs, Timescale: g.clockRate}
}

func (g *Generator) singleLoss() Material {
	id := "mat-single-loss"
	all := []model.Event{g.event(id, "v", 0), g.event(id, "v", 1), g.event(id, "v", 2)}
	// seq 1 removed from the delivered stream but covered by FEC.
	delivered := []model.Event{all[0], all[2]}
	gap := model.Gap{TrackID: "v", FirstSeq: 1, LastSeq: 1, DeclaredBy: model.StageParse}
	return Material{ID: id, Kind: KindSingleLoss, Events: delivered,
		Gaps: []model.Gap{gap}, FECGroups: []recovery.Group{fecGroup(1, all, []uint64{0, 1, 2})},
		Timescale: g.clockRate}
}

func (g *Generator) doubleLoss() Material {
	id := "mat-double-loss"
	all := []model.Event{g.event(id, "v", 0), g.event(id, "v", 1), g.event(id, "v", 2), g.event(id, "v", 3)}
	delivered := []model.Event{all[2], all[3]} // seq 0,1 lost
	gap := model.Gap{TrackID: "v", FirstSeq: 0, LastSeq: 1, DeclaredBy: model.StageParse}
	return Material{ID: id, Kind: KindDoubleLoss, Events: delivered,
		Gaps: []model.Gap{gap}, FECGroups: []recovery.Group{fecGroup(2, all, []uint64{0, 1, 2, 3})},
		Timescale: g.clockRate}
}

func (g *Generator) truncatedMP4() Material {
	// A well-formed moof header whose declared size runs past the buffer end.
	raw := buildTruncatedFragment()
	return Material{ID: "mat-truncated", Kind: KindTruncatedMP4, MP4Raw: raw, Timescale: g.clockRate}
}

func (g *Generator) badTimescale() Material {
	evs := []model.Event{g.event("mat-bad-ts", "v", 0)}
	return Material{ID: "mat-bad-ts", Kind: KindBadTimescale, Events: evs, Timescale: 0}
}

func (g *Generator) missingReference() Material {
	playlist := "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:0\n" +
		"#EXTINF:4.0,\nseg0.ts\n#EXTINF:4.0,\nmissing:seg1.ts\n"
	evs := []model.Event{g.event("mat-missing", "v", 0)}
	gap := model.Gap{TrackID: "v", FirstSeq: 1, LastSeq: 1, DeclaredBy: model.StageParse}
	return Material{ID: "mat-missing", Kind: KindMissingRef, Events: evs,
		Gaps: []model.Gap{gap}, HLSPlaylist: playlist, Timescale: g.clockRate}
}

func (g *Generator) colorClash() Material {
	evs := []model.Event{g.event("mat-color", "v", 0), g.event("mat-color", "v", 1)}
	// primaries 9 (known BT.2020) and 999 (unknown, must be quarantined).
	return Material{ID: "mat-color", Kind: KindColorClash, Events: evs,
		ColorDecls: []int{9, 999}, Timescale: g.clockRate}
}

// LatencySamples deterministically generates n synthetic stage latencies in
// milliseconds for the p95 metric. Values are always finite and positive.
func (g *Generator) LatencySamples(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		// Bounded 1..100 ms, fully determined by the seed.
		out[i] = 1 + float64(g.rng.Intn(1000))/10.0
	}
	return out
}
