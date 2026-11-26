package ebml

import (
	"bytes"

	ebmlgo "github.com/at-wat/ebml-go"
)

// CrossValidateBlock re-parses one block's raw payload with the
// github.com/at-wat/ebml-go reference implementation and checks its track
// number and relative timecode against the streaming scanner. Only the
// single block payload is read, so memory stays bounded. A disagreement is
// reported as an invalid-container error pinned to the element offset.
//
// rawBlock is the full element payload (block header + frame) that the
// scanner already read; clusterTC is the owning cluster's timecode in
// Matroska timecode units, and scale the nanoseconds-per-timecode factor.
func CrossValidateBlock(rawBlock []byte, parsed Block, clusterTC, scale int64) error {
	blk, err := ebmlgo.UnmarshalBlock(bytes.NewReader(rawBlock), int64(len(rawBlock)))
	if err != nil {
		return &Error{ErrInvalidEBML, parsed.ElemOffset,
			"ebml-go block parse disagreed: " + err.Error()}
	}
	if blk.TrackNumber != parsed.TrackNo {
		return &Error{ErrInvalidEBML, parsed.ElemOffset, "ebml-go track number mismatch"}
	}
	wantRel := parsed.TimeNs/scale - clusterTC
	if int64(blk.Timecode) != wantRel {
		return &Error{ErrInvalidEBML, parsed.ElemOffset, "ebml-go relative timecode mismatch"}
	}
	return nil
}
