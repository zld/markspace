// Package index builds a sparse random-access map from sample blocks onto
// the unified media time base and answers half-open interval queries.
// Construction streams block-by-block: memory scales with the number of
// index entries, not the PCM payload size.
package index

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"

	"mediakernel/internal/clock"
	"mediakernel/internal/wav"
)

// BlockEntry is one sparse anchor: the frame/byte position where a block
// starts and its integer-nanosecond media time.
type BlockEntry struct {
	FrameStart int64
	FrameCount int64
	ByteOffset int64
	MediaNanos int64
}

// Index is the sparse block map plus the geometry shared with WAV reads.
type Index struct {
	Entries     []BlockEntry
	SampleRate  uint32
	Channels    uint16
	FrameSize   int64
	TotalFrames int64
	DataOffset  int64
}

// Build streams blocks from a wav.Reader through the timeline and records
// one anchor per block. The WAV payload is never held in memory whole.
func Build(r *wav.Reader, tl *clock.Timeline, blockFrames int64) (*Index, error) {
	if blockFrames <= 0 {
		return nil, clock.NewError("index", clock.CatInvalidParam,
			"blockFrames must be positive")
	}
	h := r.Header()
	iter, err := r.Blocks(blockFrames)
	if err != nil {
		return nil, err
	}
	idx := &Index{
		SampleRate:  h.SampleRate,
		Channels:    h.Channels,
		FrameSize:   h.FrameSize(),
		TotalFrames: h.Frames(),
		DataOffset:  h.DataOffset,
	}
	for {
		b, err := iter.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		nanos, err := tl.SampleToNanos(b.FrameStart)
		if err != nil {
			return nil, err
		}
		idx.Entries = append(idx.Entries, BlockEntry{
			FrameStart: b.FrameStart,
			FrameCount: b.FrameCount,
			ByteOffset: b.ByteOffset,
			MediaNanos: nanos,
		})
	}
	if len(idx.Entries) == 0 && idx.TotalFrames != 0 {
		return nil, clock.NewError("index", clock.CatTruncated,
			"no blocks indexed despite %d declared frames", idx.TotalFrames)
	}
	return idx, nil
}

// Digest returns a deterministic SHA-256 over the index geometry and every
// anchor, used for checkpoint and cache verification.
func (x *Index) Digest() string {
	h := sha256.New()
	writeInt := func(v int64) {
		var buf [8]byte
		binary.LittleEndian.PutUint64(buf[:], uint64(v))
		h.Write(buf[:])
	}
	writeU32 := func(v uint32) {
		var buf [4]byte
		binary.LittleEndian.PutUint32(buf[:], v)
		h.Write(buf[:])
	}
	writeU16 := func(v uint16) {
		var buf [2]byte
		binary.LittleEndian.PutUint16(buf[:], v)
		h.Write(buf[:])
	}
	writeU32(x.SampleRate)
	writeU16(x.Channels)
	writeInt(x.FrameSize)
	writeInt(x.TotalFrames)
	writeInt(x.DataOffset)
	writeInt(int64(len(x.Entries)))
	for _, e := range x.Entries {
		writeInt(e.FrameStart)
		writeInt(e.FrameCount)
		writeInt(e.ByteOffset)
		writeInt(e.MediaNanos)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Marshal serializes the index for checkpoint/cache persistence.
func (x *Index) Marshal() []byte {
	out := make([]byte, 0, 32+len(x.Entries)*32)
	var buf [8]byte
	put := func(v int64) []byte {
		binary.LittleEndian.PutUint64(buf[:], uint64(v))
		return buf[:]
	}
	header := []byte("MKIDX001")
	out = append(out, header...)
	binary.LittleEndian.PutUint32(buf[:4], x.SampleRate)
	out = append(out, buf[:4]...)
	binary.LittleEndian.PutUint16(buf[:2], x.Channels)
	out = append(out, buf[:2]...)
	out = append(out, put(x.FrameSize)...)
	out = append(out, put(x.TotalFrames)...)
	out = append(out, put(x.DataOffset)...)
	out = append(out, put(int64(len(x.Entries)))...)
	for _, e := range x.Entries {
		out = append(out, put(e.FrameStart)...)
		out = append(out, put(e.FrameCount)...)
		out = append(out, put(e.ByteOffset)...)
		out = append(out, put(e.MediaNanos)...)
	}
	return out
}

// Unmarshal restores an index previously produced by Marshal.
func Unmarshal(data []byte) (*Index, error) {
	const header = "MKIDX001"
	if len(data) < len(header)+30 {
		return nil, clock.NewError("index", clock.CatCheckpoint,
			"serialized index too short: %d bytes", len(data))
	}
	if string(data[:len(header)]) != header {
		return nil, clock.NewError("index", clock.CatCheckpoint,
			"bad index magic %q", data[:len(header)])
	}
	pos := len(header)
	get := func() int64 {
		v := int64(binary.LittleEndian.Uint64(data[pos:]))
		pos += 8
		return v
	}
	x := &Index{}
	x.SampleRate = binary.LittleEndian.Uint32(data[pos:])
	pos += 4
	x.Channels = binary.LittleEndian.Uint16(data[pos:])
	pos += 2
	x.FrameSize = get()
	x.TotalFrames = get()
	x.DataOffset = get()
	n := get()
	if n < 0 || int(n)*32 > len(data)-pos {
		return nil, clock.NewError("index", clock.CatCheckpoint,
			"index entry count %d inconsistent with payload", n)
	}
	x.Entries = make([]BlockEntry, 0, n)
	for i := int64(0); i < n; i++ {
		x.Entries = append(x.Entries, BlockEntry{
			FrameStart: get(),
			FrameCount: get(),
			ByteOffset: get(),
			MediaNanos: get(),
		})
	}
	return x, nil
}
