// Package index builds a random-access MP4 container from recovered samples
// and independently re-parses it to verify every sample boundary stays within
// the file. All offsets and lengths are bounds-checked with overflow guards.
package index

import (
	"crypto/sha256"
	"encoding/binary"

	"mediasync/internal/clock"
	"mediasync/internal/repair"
)

// IndexEntry is one random-access capable sample record.
type IndexEntry struct {
	SampleIndex int
	IsKeyframe  bool
	Offset      int64 // absolute byte offset of sample data inside the file
	Size        int64
	MediaNS     int64
}

// MP4Index is the verified random-access table for one rendition.
type MP4Index struct {
	TrackID          uint32
	Entries          []IndexEntry
	RandomAccess     []int // sample indices that are keyframes
	MDATDataOffset   int64
	MDATPayloadBytes int64
	FileSize         int64
	SHA256           [32]byte
	RejectedSamples  int
	FirstFailure     *clock.Error
}

// Builder assembles one track's MP4 bytes and its in-memory index.
type Builder struct{ trackID uint32 }

// NewBuilder constructs an index builder.
func NewBuilder(trackID uint32) *Builder { return &Builder{trackID: trackID} }

// Build serializes samples into an MP4 byte stream. Isolated/synthetic or
// zero-size samples are excluded from the sample tables and accounted as
// rejections. Every offset is checked against the running file bound.
func (b *Builder) Build(samples []repair.Sample, durations []int64, timescale uint32) (*MP4Index, []byte, *clock.Error) {
	if len(samples) > clock.MaxIndexEntries {
		return nil, nil, clock.NewError(clock.ErrOverflow, "sample count exceeds MaxIndexEntries")
	}
	idx := &MP4Index{TrackID: b.trackID}
	payloads := make([][]byte, 0, len(samples))
	var payloadOffset int64
	for i, s := range samples {
		if s.Isolated || s.Synthetic || s.SizeBytes <= 0 {
			idx.RejectedSamples++
			if idx.FirstFailure == nil {
				ce := clock.NewError(clock.ErrMissingKeyframe, "sample excluded from index")
				ce.Sequence = s.Seq
				idx.FirstFailure = ce
			}
			continue
		}
		size := int64(s.SizeBytes)
		if err := clock.CheckOffset(payloadOffset, size, clock.MaxMP4FileBytes); err != nil {
			return nil, nil, err
		}
		payload := make([]byte, size)
		payload[0] = byte(s.Seq)
		payloads = append(payloads, payload)
		idx.Entries = append(idx.Entries, IndexEntry{
			SampleIndex: i, IsKeyframe: s.IsKeyframe,
			Offset: payloadOffset, Size: size, MediaNS: s.MediaNS,
		})
		if s.IsKeyframe {
			idx.RandomAccess = append(idx.RandomAccess, i)
		}
		payloadOffset += size
	}
	idx.MDATPayloadBytes = payloadOffset

	data, err := b.serialize(samples, durations, payloads, timescale, idx)
	if err != nil {
		return nil, nil, err
	}
	idx.FileSize = int64(len(data))
	idx.SHA256 = sha256.Sum256(data)
	return idx, data, nil
}

// serialize emits ftyp, a minimal moov and mdat. The mdat payload base offset
// is captured and every entry is rebased to an absolute, bounds-checked offset.
func (b *Builder) serialize(samples []repair.Sample, durations []int64, payloads [][]byte, timescale uint32, idx *MP4Index) ([]byte, *clock.Error) {
	moov := b.buildMoov(samples, durations, payloads, timescale)
	mdatHeader := boxHeader("mdat", 8)
	mdatSize := int64(8)
	for _, p := range payloads {
		mdatSize, _ = clock.Add64(mdatSize, int64(len(p)))
	}
	binary.BigEndian.PutUint32(mdatHeader[0:4], uint32(mdatSize))

	total := int64(len(ftypBox()) + len(moov) + len(mdatHeader))
	for _, p := range payloads {
		total, _ = clock.Add64(total, int64(len(p)))
	}
	if err := clock.CheckOffset(0, total, clock.MaxMP4FileBytes); err != nil {
		return nil, err
	}
	out := make([]byte, 0, total)
	out = append(out, ftypBox()...)
	out = append(out, moov...)
	payloadBase := int64(len(out) + len(mdatHeader))
	out = append(out, mdatHeader...)
	for _, p := range payloads {
		out = append(out, p...)
	}
	idx.MDATDataOffset = payloadBase
	bound := payloadBase + idx.MDATPayloadBytes
	for i := range idx.Entries {
		abs, err := clock.Add64(payloadBase, idx.Entries[i].Offset)
		if err != nil {
			return nil, err
		}
		if err := clock.CheckOffset(abs, idx.Entries[i].Size, bound); err != nil {
			return nil, err
		}
		idx.Entries[i].Offset = abs
	}
	return out, nil
}

// boxHeader returns an 8-byte size+type header placeholder.
func boxHeader(typ string, size uint32) []byte {
	h := make([]byte, 8)
	binary.BigEndian.PutUint32(h[0:4], size)
	copy(h[4:8], typ)
	return h
}

func uint32BE(v int64) [4]byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(v))
	return b
}
