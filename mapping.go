package mediacore

import (
	"errors"
	"sort"
)

// CoveredRange is a half-open nanosecond interval [Start, End) on one track.
type CoveredRange struct {
	Start Nano
	End   Nano
}

// Item is one mapped media fragment: the manifest reference plus its parsed
// segment view and the sources that cover the same logical interval.
type Item struct {
	Asset   string
	Version string
	TrackID uint32
	Seq     uint32
	Ref     *SegmentRef
	Range   CoveredRange
	Info    *SegmentInfo
	Payload []byte
	Sources []SourceMeta
	Status  string // pending|processed|absorbed|rejected
	Reason  *Error
	Finger  [32]byte
	IsRTP   bool
}

// Key is the stable identity of a logical fragment slot.
type Key struct {
	TrackID uint32
	Seq     uint32
}

// Map builds versioned items from parsed playlists and their segment views.
// Inputs are grouped per (track, sequence); missing files are reported rather
// than guessed. Output order is deterministic: (track, seq, version).
func Map(playlists []*Playlist, loader Loader) ([]*Item, []*Error) {
	var items []*Item
	var problems []*Error
	byKey := map[Key][]*Item{}
	for _, pl := range playlists {
		for _, tr := range pl.Tracks {
			for _, ref := range tr.Segments {
				it := &Item{Asset: pl.AssetID, Version: tr.Version, TrackID: tr.ID,
					Seq: ref.Seq, Ref: ref, Status: "pending",
					Range: CoveredRange{Start: 0, End: ref.Duration}}
				data, info, lerr := loader.Load(pl.AssetID, tr, ref)
				if lerr != nil {
					it.Status = "rejected"
					it.Reason = ReStage(lerr, pl.AssetID, tr.ID, StageMapping, ref.ByteOff).(*Error)
					problems = append(problems, it.Reason)
					items = append(items, it)
					continue
				}
				it.Payload, it.Info = data, info
				if info != nil {
					// Media-payload fingerprint governs idempotency; container or
					// color-box differences are reconciled separately.
					it.Finger = info.SHA256
					it.Range = CoveredRange{Start: info.BaseTime, End: info.BaseTime + info.Duration}
				} else {
					it.Finger = FingerprintPayload(data)
				}
				if ref.Kind == "rtp" {
					it.IsRTP = true
				}
				byKey[Key{tr.ID, ref.Seq}] = append(byKey[Key{tr.ID, ref.Seq}], it)
				items = append(items, it)
			}
		}
	}
	// Attach every covering source and sort deterministically.
	for k, group := range byKey {
		sort.SliceStable(group, func(i, j int) bool { return group[i].Version < group[j].Version })
		for _, it := range group {
			it.Sources = make([]SourceMeta, 0, len(group))
			for _, g := range group {
				it.Sources = append(it.Sources, SourceMeta{Asset: g.Asset, Version: g.Version, Track: k.TrackID})
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].TrackID != items[j].TrackID {
			return items[i].TrackID < items[j].TrackID
		}
		if items[i].Seq != items[j].Seq {
			return items[i].Seq < items[j].Seq
		}
		return items[i].Version < items[j].Version
	})
	return items, problems
}

// Loader abstracts local sample retrieval so the core library never touches
// the filesystem directly; tests and the demo supply deterministic loaders.
type Loader interface {
	Load(asset string, tr *TrackDecl, ref *SegmentRef) (raw []byte, info *SegmentInfo, err error)
}

// GroupByKey returns one slice of covering items per logical fragment slot.
func GroupByKey(items []*Item) map[Key][]*Item {
	out := map[Key][]*Item{}
	for _, it := range items {
		k := Key{it.TrackID, it.Seq}
		out[k] = append(out[k], it)
	}
	return out
}

// MissingReport derives the missing segment map directly from rejected items
// that carry ErrMissingSegment, so a single-fragment hole is still reported.
func MissingReport(items []*Item) map[uint32][]uint32 {
	out := map[uint32][]uint32{}
	seen := map[Key]bool{}
	for _, it := range items {
		if it.Reason == nil || !errors.Is(it.Reason, ErrMissingSegment) {
			continue
		}
		k := Key{it.TrackID, it.Seq}
		if !seen[k] {
			seen[k] = true
			out[it.TrackID] = append(out[it.TrackID], it.Seq)
		}
	}
	for t := range out {
		sort.Slice(out[t], func(i, j int) bool { return out[t][i] < out[t][j] })
	}
	return out
}

// MissingSeqs reports sequence holes per track over [first, last] inclusive.
func MissingSeqs(items []*Item) map[uint32][]uint32 {
	seen := map[uint32]map[uint32]bool{}
	lo, hi := map[uint32]uint32{}, map[uint32]uint32{}
	for _, it := range items {
		if seen[it.TrackID] == nil {
			seen[it.TrackID] = map[uint32]bool{}
			lo[it.TrackID], hi[it.TrackID] = it.Seq, it.Seq
		}
		seen[it.TrackID][it.Seq] = true
		if it.Seq < lo[it.TrackID] {
			lo[it.TrackID] = it.Seq
		}
		if it.Seq > hi[it.TrackID] {
			hi[it.TrackID] = it.Seq
		}
	}
	missing := map[uint32][]uint32{}
	for track, set := range seen {
		for s := lo[track]; s <= hi[track]; s++ {
			if !set[s] {
				missing[track] = append(missing[track], s)
			}
		}
	}
	return missing
}
