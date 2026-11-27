// Package fingerprint normalizes frame payloads and identifies duplicate
// segment content. A fingerprint is computed only after canonical
// normalization, so byte-level differences that normalization removes do not
// hide real duplicates. The first segment carrying a given fingerprint is
// the canonical original; later ones reference it while keeping their own
// time range and input provenance.
package fingerprint

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"

	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/frame"
	"example.com/continuity/internal/segment"
)

// Digest is a content fingerprint.
type Digest [sha256.Size]byte

// Normalize canonicalizes a frame payload: trailing padding bytes (the
// frame record's declared width/height can leave rows padded) are trimmed to
// the declared pixel count, and every pixel row is length-prefixed. The
// output is deterministic for identical image content.
func Normalize(rec *frame.Record) ([]byte, error) {
	if rec == nil {
		return nil, fault.New(fault.StageFingerprint, "normalize", errors.New("nil record"))
	}
	if rec.Width == 0 || rec.Height == 0 {
		return nil, fault.New(fault.StageFingerprint, "normalize",
			errors.New("zero frame dimension")).AtFrame(int64(rec.Seq))
	}
	// One byte per pixel for the generated grayscale frames.
	want := int(rec.Width) * int(rec.Height)
	payload := rec.Payload
	if len(payload) > want {
		payload = payload[:want]
	}
	out := make([]byte, 0, len(payload)+int(rec.Height)*4)
	width := int(rec.Width)
	for y := 0; y < int(rec.Height); y++ {
		start := y * width
		end := start + width
		if start > len(payload) {
			break
		}
		if end > len(payload) {
			end = len(payload)
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(end-start))
		out = append(out, lenBuf[:]...)
		out = append(out, payload[start:end]...)
	}
	return out, nil
}

// FrameFingerprint returns the digest of one normalized frame.
func FrameFingerprint(rec *frame.Record) (Digest, error) {
	norm, err := Normalize(rec)
	if err != nil {
		return Digest{}, err
	}
	return sha256.Sum256(norm), nil
}

// SegmentFingerprint folds the ordered normalized frames of a segment into a
// single digest. Frame order is part of the identity (each normalized frame
// is hashed in sequence), but frame sequence numbers are excluded so a
// repeated segment at a different time range still produces the same digest.
func SegmentFingerprint(seg *segment.Segment) (Digest, error) {
	h := sha256.New()
	for _, rec := range seg.Frames {
		norm, err := Normalize(rec)
		if err != nil {
			return Digest{}, err
		}
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(norm)))
		if _, err := h.Write(lenBuf[:]); err != nil {
			return Digest{}, fault.New(fault.StageFingerprint, "hash", err)
		}
		if _, err := h.Write(norm); err != nil {
			return Digest{}, fault.New(fault.StageFingerprint, "hash", err)
		}
	}
	var out Digest
	copy(out[:], h.Sum(nil))
	return out, nil
}

// Kind classifies a deduplicated segment.
type Kind uint8

const (
	// KindOriginal is the first segment carrying a fingerprint.
	KindOriginal Kind = iota + 1
	// KindDuplicate references an earlier original segment.
	KindDuplicate
)

// Result is one dedup decision for a segment.
type Result struct {
	SegmentIndex int
	Kind         Kind
	Digest       Digest
	// OriginalIndex is the canonical segment this one points at. For an
	// original it equals SegmentIndex.
	OriginalIndex int
	// Time range and provenance are retained even for duplicates.
	FrameStart  uint64
	FrameEnd    uint64
	StartNS     int64
	EndNS       int64
	InputOffset int64
}

// Deduper keeps the first occurrence of every content fingerprint, at frame
// granularity. Segment-level Apply then classifies a segment as a duplicate
// when all of its frames have been seen before, referencing the segment that
// first introduced that content.
type Deduper struct {
	firstFrame map[Digest]uint64 // fingerprint -> first frame sequence number
	frameOwner map[uint64]int    // frame sequence number -> owning segment index
}

// NewDeduper returns an empty deduper.
func NewDeduper() *Deduper {
	return &Deduper{
		firstFrame: map[Digest]uint64{},
		frameOwner: map[uint64]int{},
	}
}

// ObserveFrame records one frame's normalized fingerprint and returns true
// when the frame's content has already been observed earlier.
func (d *Deduper) ObserveFrame(rec *frame.Record) (Digest, bool, error) {
	digest, err := FrameFingerprint(rec)
	if err != nil {
		return Digest{}, false, err
	}
	_, dup := d.firstFrame[digest]
	if !dup {
		d.firstFrame[digest] = rec.Seq
	}
	return digest, dup, nil
}

// RegisterOwner binds a frame sequence number to the segment that contains it.
func (d *Deduper) RegisterOwner(frameSeq uint64, segmentIndex int) {
	d.frameOwner[frameSeq] = segmentIndex
}

// Apply classifies one segment. Segments must be presented in order. A
// segment is a duplicate when every one of its frames was first observed in
// an earlier segment; otherwise it is an original.
func (d *Deduper) Apply(seg *segment.Segment) (*Result, error) {
	digest, err := SegmentFingerprint(seg)
	if err != nil {
		return nil, err
	}
	res := &Result{
		SegmentIndex: seg.Index,
		Digest:       digest,
		FrameStart:   seg.FrameStart,
		FrameEnd:     seg.FrameEnd,
		StartNS:      seg.StartNS,
		EndNS:        seg.EndNS,
	}
	if len(seg.Frames) > 0 {
		res.InputOffset = seg.Frames[0].Offset
	}
	allDup := len(seg.Frames) > 0
	originalIndex := seg.Index
	for _, rec := range seg.Frames {
		fp, err := FrameFingerprint(rec)
		if err != nil {
			return nil, err
		}
		firstSeq, seen := d.firstFrame[fp]
		// A frame is repeated content only when its first occurrence is
		// owned by a different, already-classified segment. Frames of the
		// current segment have no owner yet (registration happens below).
		if !seen {
			allDup = false
			break
		}
		owner, owned := d.frameOwner[firstSeq]
		if !owned || owner == seg.Index {
			allDup = false
			break
		}
		originalIndex = owner
	}
	if allDup {
		res.Kind, res.OriginalIndex = KindDuplicate, originalIndex
		return res, nil
	}
	// Original segment: claim ownership of its frames so later duplicates
	// can reference this segment.
	for _, rec := range seg.Frames {
		d.frameOwner[rec.Seq] = seg.Index
	}
	res.Kind, res.OriginalIndex = KindOriginal, seg.Index
	return res, nil
}
