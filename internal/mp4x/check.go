// Package mp4x cross-checks a rebuilt Matroska random-access index against an
// MP4 index produced with github.com/abema/go-mp4. This file performs the
// round-trip verification.
package mp4x

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"

	"mediad/internal/media"
	"mediad/internal/merr"
)

// Verify builds, reopens and re-builds the MP4 to confirm the index survives
// a full round trip byte-identically.
func Verify(entries []media.IndexEntry) (CrossCheck, error) {
	data, err := Build(entries)
	if err != nil {
		return CrossCheck{}, merr.Wrap(err, "mp4x.verify", merr.CodeMP4CrossCheck, -1)
	}
	nums, dur, err := Reopen(data)
	if err != nil {
		return CrossCheck{}, merr.Wrap(err, "mp4x.verify", merr.CodeMP4CrossCheck, -1)
	}
	if len(nums) != len(entries) {
		return CrossCheck{}, merr.New("mp4x.verify", merr.CodeMP4CrossCheck, -1,
			"stss has %d entries, want %d", len(nums), len(entries))
	}
	for i, n := range nums {
		if n != uint32(i+1) {
			return CrossCheck{}, merr.New("mp4x.verify", merr.CodeMP4CrossCheck, -1,
				"stss[%d] = %d, want %d", i, n, i+1)
		}
	}
	again, err := Build(entries)
	if err != nil {
		return CrossCheck{}, merr.Wrap(err, "mp4x.verify", merr.CodeMP4CrossCheck, -1)
	}
	if !bytes.Equal(data, again) {
		return CrossCheck{}, merr.New("mp4x.verify", merr.CodeMP4CrossCheck, -1,
			"index did not round-trip byte-identically")
	}
	sum := sha256.Sum256(data)
	return CrossCheck{
		Entries:       len(nums),
		Duration:      dur,
		ByteIdentical: true,
		Digest:        hex.EncodeToString(sum[:8]),
	}, nil
}
