package mux

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
)

// File is a fully assembled fixture held in memory before being written.
type File struct {
	// chunkOff[i] is the absolute file offset of chunk i for every track;
	// tracks are interleaved per chunk index.
	chunkOff [][]uint64
	data     []byte // whole mdat payload
	tracks   []TrackSpec
}

// Build assembles tracks into an in-memory MP4 image. Sample payloads are
// generated deterministically when SampleSpec.Payload is nil.
func Build(tracks []TrackSpec, fault Faults) (*File, error) {
	if len(tracks) == 0 {
		return nil, fmt.Errorf("mux: at least one track is required")
	}
	var data bytes.Buffer
	chunkOff := make([][]uint64, len(tracks))
	for ti := range tracks {
		tr := &tracks[ti]
		if tr.Timescale == 0 {
			tr.Timescale = 25
		}
		if len(tr.Samples) == 0 {
			return nil, fmt.Errorf("mux: track %d has no samples", ti)
		}
		chunkOff[ti] = make([]uint64, len(tr.Samples))
		for si, sp := range tr.Samples {
			if sp.Delta == 0 {
				return nil, fmt.Errorf("mux: track %d sample %d has zero delta", ti, si)
			}
			payload := sp.Payload
			if payload == nil {
				payload = sampleBytes(ti, si)
			}
			chunkOff[ti][si] = uint64(data.Len())
			data.Write(payload)
		}
	}
	return &File{chunkOff: chunkOff, data: data.Bytes(), tracks: tracks}, nil
}

// sampleBytes returns deterministic, size-stable synthetic media bytes.
func sampleBytes(track, sample int) []byte {
	const size = 64
	out := make([]byte, size)
	for i := range out {
		out[i] = byte((track*31 + sample*17 + i*7) & 0xff)
	}
	return out
}

// WriteTo serializes the image as ftyp+moov+mdat. When a structural fault is
// requested the stream is cut at the documented point so readers see a
// truncated container rather than failing inside the generator.
func (f *File) WriteTo(w io.Writer, fault Faults) (int64, error) {
	cw := &countWriter{w: w}
	if err := f.writeFtyp(cw); err != nil {
		return cw.n, err
	}
	// mdat comes after moov; remember where its header must start.
	moov := f.buildMoov(fault)
	if fault == FaultTruncateStbl {
		// Emit only the front half of moov, cutting inside the sample table.
		cut := len(moov) / 2
		if _, err := cw.Write(moov[:cut]); err != nil {
			return cw.n, err
		}
		return cw.n, io.ErrUnexpectedEOF
	}
	// ftyp is 24+4=28 bytes, then moov, then the 8-byte mdat header. Build
	// stores mdat-relative offsets, so the absolute payload base is the end of
	// the mdat header.
	mdatPayloadBase := uint64(28+len(moov)) + 8
	f.patchOffsets(moov, mdatPayloadBase)
	if _, err := cw.Write(moov); err != nil {
		return cw.n, err
	}
	if err := f.writeMdat(cw); err != nil {
		return cw.n, err
	}
	return cw.n, nil
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func (f *File) writeFtyp(w io.Writer) error {
	b := newBox("ftyp")
	b.bytes([]byte("isom")) // major brand
	b.u32(0x200)             // minor version
	b.bytes([]byte("isom"))
	b.bytes([]byte("iso2"))
	b.bytes([]byte("mp41"))
	_, err := b.emit(w)
	return err
}

func (f *File) writeMdat(w io.Writer) error {
	b := newBox("mdat")
	b.bytes(f.data)
	_, err := b.emit(w)
	return err
}

// buildMoov constructs the whole moov box as one byte slice so stco offsets can
// be patched in after the layout is known.
func (f *File) buildMoov(fault Faults) []byte {
	var moov bytes.Buffer
	mvhd := childBox("mvhd", func(b *boxWriter) {
		b.fullBox(0)
		b.zero(8) // creation/modification time v0
		b.u32(1000)
		b.u32(f.movieDuration())
		b.u32(0x00010000) // rate 1.0
		b.u16(0x0100)     // volume
		b.u16(0)
		b.zero(8)
		b.bytes(identityMatrix())
		b.zero(24) // pre-defined
		b.u32(uint32(len(f.tracks) + 1))
	})
	moov.Write(mvhd)
	for ti := range f.tracks {
		moov.Write(f.buildTrak(ti, fault))
	}
	return childBox("moov", func(b *boxWriter) { b.bytes(moov.Bytes()) })
}

func identityMatrix() []byte {
	m := make([]byte, 36)
	binary.BigEndian.PutUint32(m[0:], 0x00010000)
	binary.BigEndian.PutUint32(m[16:], 0x00010000)
	binary.BigEndian.PutUint32(m[32:], 0x40000000)
	return m
}

func (f *File) movieDuration() uint32 {
	// Use the longest track duration mapped onto the 1000 Hz movie timescale.
	var maxTicks uint32
	for ti := range f.tracks {
		tr := &f.tracks[ti]
		var sum uint64
		for _, s := range tr.Samples {
			sum += uint64(s.Delta)
		}
		dur := uint32(sum * 1000 / uint64(tr.Timescale))
		if dur > maxTicks {
			maxTicks = dur
		}
	}
	return maxTicks
}

func (f *File) buildTrak(track int, fault Faults) []byte {
	tr := &f.tracks[track]
	tkhd := childBox("tkhd", func(b *boxWriter) {
		b.fullBox(0x000007) // enabled, in movie, in preview
		b.zero(8)
		b.u32(uint32(track + 1)) // track id
		b.u32(0)
		var sum uint64
		for _, s := range tr.Samples {
			sum += uint64(s.Delta)
		}
		b.u32(uint32(sum))
		b.zero(8)
		b.u16(0)
		b.u16(0) // layer, alternate group
		b.u16(0)
		b.u16(0) // volume, reserved
		b.bytes(identityMatrix())
		b.u32(320 << 16)
		b.u32(240 << 16)
	})
	mdia := childBox("mdia", func(b *boxWriter) {
		b.bytes(f.buildMdhd(track, fault))
		b.bytes(f.buildHdlr(track))
		b.bytes(f.buildMinf(track, fault))
	})
	return childBox("trak", func(b *boxWriter) {
		b.bytes(tkhd)
		b.bytes(mdia)
	})
}

func (f *File) buildMdhd(track int, fault Faults) []byte {
	tr := &f.tracks[track]
	return childBox("mdhd", func(b *boxWriter) {
		b.fullBox(0)
		b.zero(8)
		ts := tr.Timescale
		if fault == FaultZeroTimescale {
			ts = 0
		}
		b.u32(ts)
		var sum uint64
		for _, s := range tr.Samples {
			sum += uint64(s.Delta)
		}
		b.u32(uint32(sum))
		b.u16(0x55C4) // undetermined language
		b.u16(0)
	})
}
