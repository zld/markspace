// Package ingest validates the mfhd sequence structure of a fragment group:
// missing sequence numbers become recoverable gaps, duplicate numbers merge
// only when content is byte-identical, and content conflicts abort the group.
package ingest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"sort"

	"mediad/internal/mp4"
	"mediad/internal/timeline"
)

// Gap describes one recoverable missing sequence number.
type Gap struct {
	MissingSeq uint32
	AfterSeq   uint32 // last present sequence before the gap
}

// Group is a sequence-validated collection of fragments.
type Group struct {
	Name      string
	Fragments []*mp4.Fragment // ascending by sequence number, duplicates merged
	Gaps      []Gap
	// Merged counts duplicate fragments dropped because content matched.
	Merged int
}

// Report is the serialisable validation summary.
type Report struct {
	Group       string   `json:"group"`
	Sequences   []uint32 `json:"sequences"`
	Gaps        []Gap    `json:"gaps"`
	MergedCount int      `json:"merged_duplicates"`
}

// Summary returns the JSON-friendly validation report.
func (g *Group) Summary() Report {
	seqs := make([]uint32, 0, len(g.Fragments))
	for _, f := range g.Fragments {
		seqs = append(seqs, f.SeqNum)
	}
	return Report{Group: g.Name, Sequences: seqs, Gaps: g.Gaps, MergedCount: g.Merged}
}

// FragmentDigest hashes the normalised content of a fragment so that two
// identical fragments at different absolute file offsets share one digest.
// The trun data-offset field is location-dependent: it is rebased so the
// digest covers media content and all boxes except that relocation field.
func FragmentDigest(data []byte, f *mp4.Fragment) string {
	start, end := f.ContentRange[0], f.ContentRange[1]
	if start < 0 || end > int64(len(data)) || start >= end {
		return ""
	}
	sum := sha256.Sum256(normaliseFragment(data[start:end], f, start))
	return hex.EncodeToString(sum[:])
}

// normaliseFragment returns a copy of a fragment's moof+mdat bytes with every
// trun data-offset rewritten to zero so location alone never changes a digest.
func normaliseFragment(span []byte, f *mp4.Fragment, base int64) []byte {
	out := append([]byte(nil), span...)
	for _, tf := range f.Tracks {
		if tf.TrunBox == nil || tf.TrunFlags&mp4.FlagTrunDataOffset == 0 {
			continue
		}
		// fullbox(4) + sample_count(4) then data_offset(4).
		pos := tf.TrunBox.PayloadOff - base + 4 + 4
		if pos >= 0 && pos+4 <= int64(len(out)) {
			binary.BigEndian.PutUint32(out[pos:pos+4], 0)
		}
	}
	return out
}

// BuildGroup validates fragment ordering against mfhd sequence numbers.
// frags may arrive in any order; data is the underlying file bytes used for
// content-equality checks.
func BuildGroup(name string, frags []*mp4.Fragment, data []byte) (*Group, error) {
	bySeq := make(map[uint32]*mp4.Fragment)
	var order []uint32
	for _, f := range frags {
		if existing, ok := bySeq[f.SeqNum]; ok {
			if FragmentDigest(data, existing) != FragmentDigest(data, f) {
				return nil, timeline.New(timeline.ErrSeqConflict, f.File, f.Moof.Offset,
					"duplicate sequence %d has conflicting content", f.SeqNum)
			}
			// Identical content at another offset: merge, keep first location.
			continue
		}
		bySeq[f.SeqNum] = f
		order = append(order, f.SeqNum)
	}

	seqs := make([]uint32, 0, len(order))
	seqs = append(seqs, order...)
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	g := &Group{Name: name, Merged: len(frags) - len(bySeq)}
	for _, s := range seqs {
		g.Fragments = append(g.Fragments, bySeq[s])
	}
	for i := 1; i < len(seqs); i++ {
		for miss := seqs[i-1] + 1; miss < seqs[i]; miss++ {
			g.Gaps = append(g.Gaps, Gap{MissingSeq: miss, AfterSeq: seqs[i-1]})
		}
	}
	return g, nil
}

// HasGaps reports whether the group contains recoverable sequence gaps.
func (g *Group) HasGaps() bool { return len(g.Gaps) > 0 }
