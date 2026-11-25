package main

import (
	"bytes"
	"io"
	"path/filepath"

	go_mp4 "github.com/abema/go-mp4"

	"continuity/internal/demo"
	"continuity/internal/index"
	"continuity/internal/media"
	mp4box "continuity/internal/mp4"
)

// seekBuffer is an in-memory io.WriteSeeker for the go-mp4 writer.
type seekBuffer struct {
	buf []byte
	pos int64
}

func (w *seekBuffer) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(len(w.buf)) {
		grown := make([]byte, end)
		copy(grown, w.buf)
		w.buf = grown
	}
	copy(w.buf[w.pos:end], p)
	w.pos = end
	return len(p), nil
}

func (w *seekBuffer) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		w.pos = offset
	case io.SeekCurrent:
		w.pos += offset
	case io.SeekEnd:
		w.pos = int64(len(w.buf)) + offset
	}
	return w.pos, nil
}

// buildContainerBytes writes a minimal ftyp+moov container with the given
// brand/version so the demo can produce a real, parseable MP4 structure.
func buildContainerBytes(brand string, minor uint32, duration uint64) []byte {
	sink := &seekBuffer{}
	w := go_mp4.NewWriter(sink)

	w.StartBox(&go_mp4.BoxInfo{Type: go_mp4.BoxTypeFtyp()})
	b := [4]byte{}
	copy(b[:], brand)
	go_mp4.Marshal(w, &go_mp4.Ftyp{
		MajorBrand:       b,
		MinorVersion:     minor,
		CompatibleBrands: []go_mp4.CompatibleBrandElem{{CompatibleBrand: b}},
	}, go_mp4.Context{})
	w.EndBox()

	w.StartBox(&go_mp4.BoxInfo{Type: go_mp4.BoxTypeMoov()})
	w.StartBox(&go_mp4.BoxInfo{Type: go_mp4.BoxTypeMvhd()})
	mvhd := &go_mp4.Mvhd{Timescale: 48000}
	mvhd.DurationV0 = uint32(duration)
	go_mp4.Marshal(w, mvhd, go_mp4.Context{})
	w.EndBox()
	w.EndBox()
	return sink.buf
}

// syntheticContainer builds and parses a real MP4 container, returning
// its inspected metadata. The bytes are also persisted for review.
func syntheticContainer(brand string, minor uint32, seed int64) mp4box.Metadata {
	duration := uint64(demoDurationSamples())
	raw := buildContainerBytes(brand, minor, duration)
	md, err := mp4box.Inspect(bytes.NewReader(raw))
	if err != nil {
		// buildContainerBytes always emits a parseable structure.
		panic(media.WrapError(media.CatUnrecoverable, "demo: container inspect", err))
	}
	_ = seed
	return md
}

func demoDurationSamples() int {
	return demo.PacketCount * demo.SamplesPerPacket
}

// corruptCheckpoint appends an unverifiable tail record while keeping the
// valid prefix, so the next run rolls back precisely to the last valid
// boundary instead of discarding the whole index.
func corruptCheckpoint(workDir string) error {
	idxDir := filepath.Join(workDir, "index")
	if _, err := index.CorruptTailRecord(idxDir); err != nil {
		return err
	}
	return nil
}
