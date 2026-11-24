package fixture

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"

	"mediapack/internal/clock"
	"mediapack/internal/demux"
)

type TrackSpec struct {
	ID             uint32
	Kind           string // video or audio
	FourCC         string
	Timescale      uint32
	SampleDuration uint32
	SampleCount    int
	PayloadSize    int
	Width          uint16
	Height         uint16
	Channels       uint16
	SampleRate     uint32
}

type SampleRef struct {
	TrackID uint32
	Index   int
}

type MP4Spec struct {
	Tracks        []TrackSpec
	Corrupt       []SampleRef
	Oversize      []SampleRef
	OversizeBytes int
	Brand         string
}

type BuiltSample struct {
	Ref    SampleRef
	Offset int64
	Size   int
	Hash   string
}
type BuiltMP4 struct {
	Path    string
	Samples []BuiltSample
}

func WriteMP4(path string, spec MP4Spec) (*BuiltMP4, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	brand := spec.Brand
	if brand == "" {
		brand = "isom"
	}
	ftyp := buildFtyp(brand)

	type placed struct {
		ref     SampleRef
		payload []byte
		offset  int64
	}
	var data []byte
	var placedAll []placed
	base := int64(len(ftyp) + 8) // ftyp plus mdat header
	corrupt := refSet(spec.Corrupt)
	oversize := refSet(spec.Oversize)
	for _, tr := range spec.Tracks {
		for i := 0; i < tr.SampleCount; i++ {
			p := Payload(tr.ID, i, tr.PayloadSize)
			if corrupt[SampleRef{tr.ID, i}] {
				p = CorruptPayload(p)
			}
			off := base + int64(len(data))
			data = append(data, p...)
			placedAll = append(placedAll, placed{SampleRef{tr.ID, i}, p, off})
		}
	}
	mdat := box("mdat", data)

	var moov bytes.Buffer
	moov.Write(buildMvhd(1000, movieDurationMS(spec)))
	for _, tr := range spec.Tracks {
		offs := make([]uint32, tr.SampleCount)
		sizes := make([]uint32, tr.SampleCount)
		for _, p := range placedAll {
			if p.ref.TrackID != tr.ID {
				continue
			}
			offs[p.ref.Index] = uint32(p.offset)
			sizes[p.ref.Index] = uint32(len(p.payload))
			if oversize[p.ref] {
				sizes[p.ref.Index] += uint32(max(spec.OversizeBytes, 64))
			}
		}
		moov.Write(buildTrak(tr, sizes, offs))
	}
	moovBox := box("moov", moov.Bytes())

	out := bytes.NewBuffer(nil)
	out.Write(ftyp)
	out.Write(mdat)
	out.Write(moovBox)
	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		return nil, err
	}
	built := &BuiltMP4{Path: path}
	for _, p := range placedAll {
		built.Samples = append(built.Samples, BuiltSample{
			Ref: p.ref, Offset: p.offset, Size: len(p.payload), Hash: hashForTest(p.payload),
		})
	}
	return built, nil
}

func WriteTruncatedBox(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b bytes.Buffer
	b.Write(buildFtyp("isom"))
	// Declare a 4 KiB free box but provide only a few payload bytes.
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[:4], 4096)
	copy(hdr[4:8], "free")
	b.Write(hdr)
	b.Write([]byte("partial"))
	return os.WriteFile(path, b.Bytes(), 0o644)
}

func refSet(refs []SampleRef) map[SampleRef]bool {
	m := make(map[SampleRef]bool, len(refs))
	for _, r := range refs {
		m[r] = true
	}
	return m
}

func movieDurationMS(spec MP4Spec) uint32 {
	var max clock.Rational
	for _, tr := range spec.Tracks {
		d := clock.Ticks(int64(tr.SampleCount)*int64(tr.SampleDuration), clock.TickRate(tr.Timescale))
		if d.Cmp(max) > 0 {
			max = d
		}
	}
	return uint32(max.Mul(clock.New(1000, 1)).Float64())
}

func hashForTest(p []byte) string { return demux.HashPayload(p) }
