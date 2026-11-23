package repair

import (
	"fmt"
	"sort"

	"mediad/internal/media"
	"mediad/internal/merr"
)

// IndexOptions tunes index construction.
type IndexOptions struct {
	// MinEntries is the number of keyframes below which the index is
	// considered unusable. Default 10, matching the acceptance target.
	MinEntries int
}

func (o IndexOptions) withDefaults() IndexOptions {
	if o.MinEntries <= 0 {
		o.MinEntries = 10
	}
	return o
}

// Cursor records how far a damaged input was confirmed, so a later run can
// resume without re-reading the damaged tail.
type Cursor struct {
	// ConfirmedBytes is the byte offset just past the last fully confirmed
	// element. It is always a valid parse boundary.
	ConfirmedBytes int64
	// Blocks is the number of blocks confirmed before the cursor.
	Blocks int
	// Entries is the number of index entries confirmed before the cursor.
	Entries int
	// Damaged reports that the cursor was produced by a damaged input.
	Damaged bool
	// Cause is the error code that stopped the read.
	Cause merr.Code
}

// IndexResult is a built index plus its recovery cursor.
type IndexResult struct {
	Index  media.Index
	Cursor Cursor
	// KeyframeBlocks is the number of keyframe blocks seen, including those
	// without a usable offset.
	KeyframeBlocks int
}

// BuildIndex rebuilds a random-access index from the video keyframes in a
// repaired stream.
//
// The index is a pure function of the block list: the same blocks always
// produce the same entries and the same digest. Entries only ever reference
// blocks that carry the keyframe flag.
func BuildIndex(blocks []media.Block, opts IndexOptions) (IndexResult, error) {
	opts = opts.withDefaults()
	var entries []media.IndexEntry
	keyframes := 0
	var lastConfirmed int64
	for _, b := range blocks {
		if b.EndOffset > lastConfirmed {
			lastConfirmed = b.EndOffset
		}
		if b.TrackType != media.TrackVideo || !b.Keyframe {
			continue
		}
		keyframes++
		if b.ElementOffset <= 0 {
			continue
		}
		entries = append(entries, media.IndexEntry{
			Track:         b.Track,
			TimeNs:        b.TimeNs,
			ByteOffset:    b.ElementOffset,
			ClusterOffset: b.ClusterOffset,
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Track != entries[j].Track {
			return entries[i].Track < entries[j].Track
		}
		return entries[i].TimeNs < entries[j].TimeNs
	})
	res := IndexResult{
		Index:          media.Index{Entries: entries},
		KeyframeBlocks: keyframes,
	}
	res.Index.Digest = DigestIndex(entries)
	res.Cursor = Cursor{
		ConfirmedBytes: lastConfirmed,
		Blocks:         len(blocks),
		Entries:        len(entries),
	}
	if err := ValidateIndex(res.Index); err != nil {
		return res, err
	}
	return res, nil
}

// BuildIndexFromRepair is a convenience wrapper that adds the prefix policy:
// when the input was damaged, the index keeps every confirmed entry and the
// cursor records where to resume.
//
// The returned error is always damage-tagged (merr.IsDamage true) so the CLI
// can distinguish "summarized a damaged input" from a programming fault.
func BuildIndexFromRepair(res *Result, damage error, opts IndexOptions) (IndexResult, error) {
	out, err := BuildIndex(res.Blocks, opts)
	if damage != nil {
		out.Cursor.Damaged = true
		out.Cursor.Cause = merr.CodeOf(damage)
	}
	if err != nil {
		// Invariant violations are damage too: keep the entries we have.
		out.Cursor.Damaged = true
		out.Cursor.Cause = merr.CodeOf(err)
		return out, err
	}
	if damage == nil && len(out.Index.Entries) < opts.withDefaults().MinEntries {
		err := merr.New("index.build", merr.CodeNoVideoKeyframes, -1,
			"only %d keyframe entries, need %d", len(out.Index.Entries), opts.withDefaults().MinEntries)
		out.Cursor.Damaged = true
		out.Cursor.Cause = err.Code
		return out, err
	}
	return out, nil
}

// String renders a cursor for logs.
func (c Cursor) String() string {
	return fmt.Sprintf("confirmed=%d blocks=%d entries=%d damaged=%v cause=%s",
		c.ConfirmedBytes, c.Blocks, c.Entries, c.Damaged, c.Cause)
}
