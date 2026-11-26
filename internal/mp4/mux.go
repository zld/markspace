package mp4

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"

	"replayctl/internal/fault"
)

// TrackIn is a compact, uniformly-timed MJPEG or PCM track to mux.
type TrackIn struct {
	Kind       string // "video" or "audio"
	Codec      string // "jpeg " or "sowt"
	Timescale  uint32
	Width      uint16
	Height     uint16
	Channels   uint16
	Bits       uint16
	SampleRate uint32

	Data              []byte
	DefaultSampleSize uint32 // nonzero for PCM
	SampleSizes       []uint32
	SampleCount       uint32
	Delta             uint32
	SyncSamples       []uint32 // 1-based video sync sample numbers
}

// MuxFile writes tracks atomically via a same-directory temporary file.
func MuxFile(path string, tracks []TrackIn) error {
	if path == "" {
		return fault.New(fault.Usage, fault.StageRender, path, -1, -1, "empty output path")
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fault.New(fault.IO, fault.StageRender, path, -1, -1, "create output directory: %v", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mp4-write-*")
	if err != nil {
		return fault.New(fault.IO, fault.StageRender, path, -1, -1, "temp file: %v", err)
	}
	tmpName := tmp.Name()
	if err := tmp.Close(); err != nil {
		return fault.New(fault.IO, fault.StageRender, tmpName, -1, -1, "close temp: %v", err)
	}
	if err := WriteFile(tmpName, tracks); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fault.New(fault.IO, fault.StageRender, path, -1, -1, "atomic rename: %v", err)
	}
	return nil
}

// WriteFile writes and fsyncs tracks to an already-selected path.
func WriteFile(path string, tracks []TrackIn) error {
	if path == "" {
		return fault.New(fault.Usage, fault.StageRender, path, -1, -1, "empty output path")
	}
	for i := range tracks {
		if err := validateTrack(&tracks[i]); err != nil {
			return fault.Wrap(err, fault.InvalidBox, fault.StageRender, path, -1, -1)
		}
	}

	ftyp := buildFtyp()
	placeholderMoov := buildMoov(tracks, nil, 90_000)
	dataStart := int64(len(ftyp) + len(placeholderMoov) + 8)
	offsets := make([]uint32, len(tracks))
	var cursor int64
	for i, tr := range tracks {
		offsets[i] = uint32(dataStart + cursor)
		cursor += int64(len(tr.Data))
	}
	moov := buildMoov(tracks, offsets, 90_000)
	if len(moov) != len(placeholderMoov) {
		return fault.New(fault.Render, fault.StageRender, path, -1, -1, "internal error: moov length changed during offset fixup")
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fault.New(fault.IO, fault.StageRender, path, -1, -1, "open for write: %v", err)
	}
	mdatSize := uint32(8 + cursor)
	var head [8]byte
	binary.BigEndian.PutUint32(head[0:4], mdatSize)
	copy(head[4:8], "mdat")
	for _, chunk := range [][]byte{ftyp, moov, head[:]} {
		if _, err := f.Write(chunk); err != nil {
			f.Close()
			return fault.New(fault.IO, fault.StageRender, path, -1, -1, "write mp4: %v", err)
		}
	}
	for _, tr := range tracks {
		if _, err := f.Write(tr.Data); err != nil {
			f.Close()
			return fault.New(fault.IO, fault.StageRender, path, -1, -1, "write mdat: %v", err)
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fault.New(fault.IO, fault.StageRender, path, -1, -1, "sync mp4: %v", err)
	}
	if err := f.Close(); err != nil {
		return fault.New(fault.IO, fault.StageRender, path, -1, -1, "close mp4: %v", err)
	}
	return nil
}

func validateTrack(tr *TrackIn) error {
	if tr.Timescale == 0 || tr.SampleCount == 0 || tr.Delta == 0 {
		return fmt.Errorf("mp4: track requires timescale/count/delta")
	}
	if tr.DefaultSampleSize == 0 && len(tr.SampleSizes) != int(tr.SampleCount) {
		return fmt.Errorf("mp4: variable track requires %d sizes", tr.SampleCount)
	}
	if tr.DefaultSampleSize != 0 && len(tr.SampleSizes) != 0 {
		return fmt.Errorf("mp4: default and variable sample sizes are mutually exclusive")
	}
	expected := 0
	if tr.DefaultSampleSize != 0 {
		expected = int(tr.DefaultSampleSize) * int(tr.SampleCount)
	} else {
		for _, sz := range tr.SampleSizes {
			expected += int(sz)
		}
	}
	if expected != len(tr.Data) {
		return fmt.Errorf("mp4: data length %d does not match sample sizes %d", len(tr.Data), expected)
	}
	return nil
}

func buildFtyp() []byte {
	var b byteBuilder
	s := b.begin("ftyp")
	b.fourcc("isom")
	b.u32(512)
	b.fourcc("isom")
	b.fourcc("iso2")
	b.fourcc("mp41")
	b.end(s)
	return b
}

func buildMoov(tracks []TrackIn, offsets []uint32, movieTimescale uint32) []byte {
	var b byteBuilder
	moov := b.begin("moov")
	mvhd := b.begin("mvhd")
	b.fullBox(0, 0)
	b.u32(0) // creation
	b.u32(0) // modification
	b.u32(movieTimescale)
	b.u32(maxDuration(tracks, movieTimescale))
	b.u32(0x00010000) // rate 1.0
	b.u16(0x0100)     // volume
	b.raw(make([]byte, 10))
	writeIdentityMatrix(&b)
	b.raw(make([]byte, 24))
	b.u32(uint32(len(tracks) + 1))
	b.end(mvhd)

	for i, tr := range tracks {
		off := uint32(0)
		if offsets != nil {
			off = offsets[i]
		}
		buildTrak(&b, tr, uint32(i+1), off)
	}
	b.end(moov)
	return b
}

func maxDuration(tracks []TrackIn, movieTimescale uint32) uint32 {
	var max uint64
	for _, tr := range tracks {
		dur := uint64(tr.SampleCount) * uint64(tr.Delta)
		dur = dur * uint64(movieTimescale) / uint64(tr.Timescale)
		if dur > max {
			max = dur
		}
	}
	return uint32(max)
}

func writeIdentityMatrix(b *byteBuilder) {
	b.u32(0x00010000)
	b.u32(0)
	b.u32(0)
	b.u32(0)
	b.u32(0x00010000)
	b.u32(0)
	b.u32(0)
	b.u32(0)
	b.u32(0x40000000)
}
