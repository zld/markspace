// Package frames decodes the frame record container used by the archive
// ingest format.
//
// A frame file is a flat sequence of records:
//
//	magic   [4]byte  "MFR1"
//	version uint16   must be 1
//	flags   uint16   bit0 keyframe, bit1 end-of-stream
//	track   uint8    video track id, must be non-zero
//	stream  uint8    stream selector, always 0 for images
//	pts     uint64   presentation time in 90 kHz ticks
//	dts     uint64   decode time in 90 kHz ticks
//	size    uint32   payload length, 1..MaxPayload
//	payload [size]byte
//	digest  [32]byte SHA-256 of every preceding byte in this record
//
// Records carry no interior offsets, so the decoder tracks the absolute byte
// position itself and reports it on failure. A malformed length or a failed
// digest aborts only the offending record: the scanner resynchronises on the
// magic prefix so later locatable records are still recovered.
package frames

import (
	"encoding/binary"
	"fmt"
	"hash"
	"hash/crc32"
	"io"

	"mediarun/internal/fault"
)

// Container constants.
const (
	Magic          = "MFR1"
	Version        = 1
	HeaderSize     = 4 + 2 + 2 + 1 + 1 + 8 + 8 + 4
	DigestSize     = 32
	MaxPayload     = 4 << 20 // per-record cap guards against bogus allocations
	MaxRecordBytes = HeaderSize + MaxPayload + DigestSize
)

// Flag bits stored in the record header.
const (
	FlagKeyframe uint16 = 1 << 0
	FlagEndOfSeq uint16 = 1 << 1
)

// MediaKind distinguishes the archive sample layouts we accept.
type MediaKind uint8

const (
	MediaUnknown MediaKind = iota
	MediaImageYCbCr
	MediaImageRGBA
	MediaAudioOpus
)

// Metadata is the non-payload portion of a frame record.
type Metadata struct {
	Version   uint16
	Flags     uint16
	Track     uint8
	Stream    uint8
	PTS       uint64
	DTS       uint64
	Size      uint32
	Checksum  [DigestSize]byte
	CRC32     uint32 // convenience digest over the payload
	MediaKind MediaKind
}

// Record is a fully validated frame with its source coordinates.
type Record struct {
	Offset  int64 // absolute byte offset of the record's magic
	Index   int   // zero-based sequential index among parsed records
	Meta    Metadata
	Payload []byte
}

// Keyframe reports whether the record is marked as a random access point.
func (r *Record) Keyframe() bool { return r.Meta.Flags&FlagKeyframe != 0 }

// EndOfSequence reports whether the record closes a fragment.
func (r *Record) EndOfSequence() bool { return r.Meta.Flags&FlagEndOfSeq != 0 }

// EncodeOptions controls record synthesis used by the fixture generator.
type EncodeOptions struct {
	Version uint16
	Flags   uint16
	Track   uint8
	Stream  uint8
	PTS     uint64
	DTS     uint64
}

// Encode writes one complete record including its digest and reports how many
// bytes were written. Generators use the count to precompute fixture offsets.
func Encode(w io.Writer, opts EncodeOptions, payload []byte) (int, error) {
	if len(payload) == 0 || len(payload) > MaxPayload {
		return 0, fmt.Errorf("payload size %d outside 1..%d", len(payload), MaxPayload)
	}
	version := opts.Version
	if version == 0 {
		version = Version
	}
	meta := Metadata{
		Version: version,
		Flags:   opts.Flags,
		Track:   opts.Track,
		Stream:  opts.Stream,
		PTS:     opts.PTS,
		DTS:     opts.DTS,
		Size:    uint32(len(payload)),
	}
	hdr := HeaderBytes(meta)
	meta.Checksum = SumRecord(hdr, payload)
	digest := meta.Checksum[:]

	n := 0
	for _, part := range [][]byte{hdr, payload, digest} {
		written, err := w.Write(part)
		n += written
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

// newDigest is a seam so checksum.go can share the hasher construction.
func newDigest() hash.Hash { return sha256Hasher() }

// payloadCRC returns the auxiliary CRC-32 used in manifests.
func payloadCRC(p []byte) uint32 { return crc32.ChecksumIEEE(p) }

// HeaderBytes rebuilds the canonical header for a metadata value. It is the
// exact byte sequence covered by the record digest.
func HeaderBytes(meta Metadata) []byte {
	hdr := make([]byte, HeaderSize)
	copy(hdr[0:4], Magic)
	binary.BigEndian.PutUint16(hdr[4:6], meta.Version)
	binary.BigEndian.PutUint16(hdr[6:8], meta.Flags)
	hdr[8] = meta.Track
	hdr[9] = meta.Stream
	binary.BigEndian.PutUint64(hdr[10:18], meta.PTS)
	binary.BigEndian.PutUint64(hdr[18:26], meta.DTS)
	binary.BigEndian.PutUint32(hdr[26:30], meta.Size)
	return hdr
}

// ReadPayload reads the payload of a metadata-only record back from the
// source. It re-verifies the digest so a file that changed after the planning
// pass is detected instead of muxed.
func ReadPayload(r io.ReaderAt, rec *Record) ([]byte, error) {
	if rec == nil {
		return nil, fault.New(fault.StageParse, fault.KindIO, "nil record")
	}
	if rec.Payload != nil {
		return rec.Payload, nil
	}
	n := int(rec.Meta.Size)
	if n <= 0 || n > MaxPayload {
		e := fault.New(fault.StageParse, fault.KindLength, "declared payload %d outside limits", n)
		e.At(rec.Offset, rec.Index).WithTrack(rec.Meta.Track)
		return nil, e
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, rec.Offset+HeaderSize); err != nil {
		return nil, fault.Wrap(fault.StageParse, fault.KindIO, err, "re-read payload")
	}
	if err := VerifyDigest(rec.Offset, rec.Index, rec.Meta, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// frameErr builds a parse fault anchored at the given absolute offset. A
// truncated tail is not recoverable: nothing follows it to resynchronise on.
func frameErr(kind fault.Kind, offset int64, record int, track uint8, format string, args ...any) *fault.Error {
	e := fault.New(fault.StageParse, kind, format, args...).At(offset, record)
	if track != 0 {
		e.WithTrack(track)
	}
	e.Recoverable = kind != fault.KindLimit
	return e
}
