// Package ebml implements a bounded, streaming reader for the subset of EBML
// used by Matroska and WebM. This file decodes block headers and scalar leaf
// values.
package ebml

import (
	"encoding/binary"
	"fmt"
	"math"

	"mediad/internal/merr"
)

// parseBlock decodes the block header: track number VINT, signed 16-bit
// timecode, flags, and the opaque payload.
func (d *Demuxer) parseBlock(ev Event) (Block, error) {
	data := ev.Data
	if len(data) < 4 {
		return Block{}, merr.New("demux.block", merr.CodeInvalidBlock, ev.HeaderOffset,
			"block payload %d bytes, need at least 4", len(data))
	}
	track, n, err := readTrackVINT(data)
	if err != nil {
		return Block{}, merr.New("demux.block", merr.CodeInvalidBlock, ev.HeaderOffset,
			"track VINT: %v", err)
	}
	if len(data) < n+3 {
		return Block{}, merr.New("demux.block", merr.CodeInvalidBlock, ev.HeaderOffset,
			"block truncated after track number")
	}
	raw := int16(binary.BigEndian.Uint16(data[n : n+2]))
	flags := data[n+2]
	payload := data[n+3:]
	if len(payload) == 0 {
		return Block{}, merr.New("demux.block", merr.CodeInvalidBlock, ev.HeaderOffset,
			"block has an empty frame payload")
	}
	if d.inCluster == 0 {
		return Block{}, merr.New("demux.block", merr.CodeInvalidBlock, ev.HeaderOffset,
			"block outside Cluster")
	}
	if _, ok := d.trackType(track); !ok {
		return Block{}, merr.New("demux.block", merr.CodeInvalidBlock, ev.HeaderOffset,
			"block references undeclared track %d", track)
	}
	key := flags&0x80 != 0
	if ev.ID == IDBlock {
		// A Block's keyframe status is the absence of any ReferenceBlock.
		key = true
	}
	if ev.ID != IDSimpleBlock {
		return Block{}, merr.New("demux.block", merr.CodeInvalidBlock, ev.HeaderOffset,
			"only SimpleBlock is supported")
	}
	return Block{
		TrackNumber:   track,
		TimecodeMs:    uint32(int64(d.clusterTC) + int64(raw)),
		Keyframe:      key,
		Payload:       append([]byte(nil), payload...),
		BlockOffset:   ev.HeaderOffset,
		ClusterOffset: d.clusterOff,
		EndOffset:     ev.DataOffset + int64(len(ev.Data)),
	}, nil
}

// readTrackVINT reads the block track number, which is a plain VINT without
// the id marker retention.
func readTrackVINT(b []byte) (uint64, int, error) {
	if len(b) == 0 {
		return 0, 0, fmt.Errorf("empty")
	}
	n := vintLen(b[0])
	if n == 0 || n > 8 || len(b) < n {
		return 0, 0, fmt.Errorf("bad vint length")
	}
	v := uint64(b[0]) & (0xFF >> n)
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, n, nil
}

func trimNul(b []byte) string {
	for len(b) > 0 && b[len(b)-1] == 0 {
		b = b[:len(b)-1]
	}
	return string(b)
}

func uintValue(b []byte, off int64) (uint64, error) {
	switch {
	case len(b) == 0:
		return 0, nil
	case len(b) > 8:
		return 0, merr.New("ebml.uint", merr.CodeBadInput, off, "integer leaf of %d bytes", len(b))
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v, nil
}

func floatValue(b []byte, off int64) (float64, error) {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	case 0:
		return 0, nil
	}
	return 0, merr.New("ebml.float", merr.CodeBadInput, off, "float leaf of %d bytes", len(b))
}
