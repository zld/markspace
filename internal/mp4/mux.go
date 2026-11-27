// Package mp4 writes the normalized PCM audio into an ISO-BMFF container via
// go-mp4 and re-reads the result to verify track duration, sample count,
// average bitrate and encapsulation size.
package mp4

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"

	gomp4 "github.com/abema/go-mp4"
	"github.com/example/mediad/internal/audio"
)

// MuxConfig captures everything the container must record.
type MuxConfig struct {
	Rate          uint32
	Channels      uint16
	Codec         string // four-character sample entry type, e.g. "mp4a"
	BitsPerSample uint16 // PCM sample width in bits (16 or 24)
}

// Candidate is one encoding parameter set the size optimizer may choose.
type Candidate struct {
	BitsPerSample uint16
}

// defaultCandidates are tried in increasing size order so the first that
// satisfies the byte budget is also the smallest; quality gates are never
// relaxed because bit depth only changes container size, not loudness/peak.
var defaultCandidates = []Candidate{{BitsPerSample: 16}, {BitsPerSample: 24}}

// SmallestWithinBudget muxes each candidate and returns the smallest artifact
// that fits maxBytes. When none fit it returns the smallest (16-bit) artifact
// and ok=false so the caller marks the item unmet with its measured deviation
// rather than silently dropping quality.
func SmallestWithinBudget(b audio.Buffer, base MuxConfig, maxBytes int64) ([]byte, MuxReport, bool, error) {
	var best []byte
	var bestReport MuxReport
	fit := false
	for _, cand := range defaultCandidates {
		cfg := base
		cfg.BitsPerSample = cand.BitsPerSample
		artifact, rep, err := Mux(b, cfg)
		if err != nil {
			return nil, MuxReport{}, false, err
		}
		if best == nil || len(artifact) < len(best) {
			best, bestReport = artifact, rep
		}
		if maxBytes <= 0 || int64(len(artifact)) <= maxBytes {
			// Candidates are ordered smallest-first, so the first fit wins.
			return artifact, rep, true, nil
		}
	}
	return best, bestReport, fit, nil
}

// MuxReport is the structural verification result read back from the file.
type MuxReport struct {
	Bytes          int64   `json:"bytes"`
	TrackDuration  uint64  `json:"trackDuration"` // timescale units
	Timescale      uint32  `json:"timescale"`
	SampleCount    uint32  `json:"sampleCount"`
	SampleRate     uint32  `json:"sampleRate"`
	ChannelCount   uint16  `json:"channelCount"`
	AvgBitratebps  float64 `json:"avgBitrateBps"`
	DurationMicros uint64  `json:"durationMicros"`
}

// encodePCM converts float64 [-1,1] interleaved samples to little-endian
// integer PCM (16- or 24-bit) with deterministic saturation.
func encodePCM(b audio.Buffer, bits uint16) []byte {
	bytesPerSample := int(bits / 8)
	out := make([]byte, len(b.Samples)*bytesPerSample)
	maxPos := int32(1<<(bits-1) - 1)
	minNeg := -int32(1 << (bits - 1))
	for i, s := range b.Samples {
		v := int32(math.Round(s * float64(maxPos)))
		if v > maxPos {
			v = maxPos
		} else if v < minNeg {
			v = minNeg
		}
		u := uint32(v)
		for k := 0; k < bytesPerSample; k++ {
			out[i*bytesPerSample+k] = byte(u >> (8 * k))
		}
	}
	return out
}

// Mux writes one audio track containing the supplied PCM and returns the
// encoded bytes plus a report verified by re-parsing the container.
func Mux(b audio.Buffer, cfg MuxConfig) ([]byte, MuxReport, error) {
	if b.Ch != int(cfg.Channels) {
		return nil, MuxReport{}, fmt.Errorf("mp4: buffer channels %d != config %d", b.Ch, cfg.Channels)
	}
	bits := cfg.BitsPerSample
	if bits == 0 {
		bits = 16
	}
	pcm := encodePCM(b, bits)
	ws := &writeSeeker{}
	w := gomp4.NewWriter(ws)

	// ftyp
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: gomp4.BoxTypeFtyp()}); err != nil {
		return nil, MuxReport{}, err
	}
	if _, err := gomp4.Marshal(w, &gomp4.Ftyp{
		MajorBrand:   gomp4.BrandISOM(),
		MinorVersion: 512,
		CompatibleBrands: []gomp4.CompatibleBrandElem{
			{CompatibleBrand: gomp4.BrandISOM()},
			{CompatibleBrand: gomp4.BrandMP41()},
		},
	}, gomp4.Context{}); err != nil {
		return nil, MuxReport{}, err
	}
	if _, err := w.EndBox(); err != nil {
		return nil, MuxReport{}, err
	}

	duration := uint64(b.Frames)
	// moov > trak
	startBox(w, gomp4.BoxTypeMoov())
	startBox(w, gomp4.BoxTypeTrak())
	leaf(w, gomp4.BoxTypeTkhd(), &gomp4.Tkhd{
		FullBox:    gomp4.FullBox{Flags: [3]byte{0x00, 0x00, 0x07}}, // enabled+inMovie+inPreview
		TrackID:    1,
		DurationV0: uint32(duration),
		Volume:     0x0100,
		Matrix: [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
	})
	startBox(w, gomp4.BoxTypeMdia())
	leaf(w, gomp4.BoxTypeMdhd(), &gomp4.Mdhd{
		Timescale:  cfg.Rate,
		DurationV0: uint32(duration),
		Language:   [3]byte{'u', 'n', 'd'},
	})
	leaf(w, gomp4.BoxTypeHdlr(), &gomp4.Hdlr{
		HandlerType: [4]byte{'s', 'o', 'u', 'n'},
		Name:        "SoundHandler",
	})
	startBox(w, gomp4.BoxTypeMinf())
	leaf(w, gomp4.BoxTypeSmhd(), &gomp4.Smhd{})
	startBox(w, gomp4.BoxTypeDinf())
	startBox(w, gomp4.BoxTypeDref())
	leafPayload(w, &gomp4.Dref{EntryCount: 1})
	leaf(w, gomp4.BoxTypeUrl(), &gomp4.Url{FullBox: gomp4.FullBox{Flags: [3]byte{0x00, 0x00, 0x01}}})
	endBox(w) // dref
	endBox(w) // dinf
	startBox(w, gomp4.BoxTypeStbl())
	// stsd with one mp4a audio sample entry.
	startBox(w, gomp4.BoxTypeStsd())
	leafPayload(w, &gomp4.Stsd{EntryCount: 1})
	leaf(w, boxType(cfg.Codec), &gomp4.AudioSampleEntry{
		SampleEntry: gomp4.SampleEntry{
			AnyTypeBox:         gomp4.AnyTypeBox{Type: boxType(cfg.Codec)},
			DataReferenceIndex: 1,
		},
		ChannelCount: cfg.Channels,
		SampleSize:   bits,
		SampleRate:   cfg.Rate << 16, // fixed-point 16.16
	})
	endBox(w) // stsd
	leaf(w, gomp4.BoxTypeStts(), &gomp4.Stts{
		EntryCount: 1,
		Entries:    []gomp4.SttsEntry{{SampleCount: uint32(b.Frames), SampleDelta: 1}},
	})
	leaf(w, gomp4.BoxTypeStsc(), &gomp4.Stsc{
		EntryCount: 1,
		Entries:    []gomp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(b.Frames), SampleDescriptionIndex: 1}},
	})
	leaf(w, gomp4.BoxTypeStsz(), &gomp4.Stsz{
		SampleSize:  uint32(cfg.Channels) * uint32(bits/8),
		SampleCount: uint32(b.Frames),
	})
	leaf(w, gomp4.BoxTypeStco(), &gomp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}})
	endBox(w) // stbl
	endBox(w) // minf
	endBox(w) // mdia
	endBox(w) // trak
	endBox(w) // moov

	// mdat: capture the payload position so the stco chunk offset is exact.
	startBox(w, gomp4.BoxTypeMdat())
	dataOffset := uint32(ws.Len())
	if _, err := w.Write(pcm); err != nil {
		return nil, MuxReport{}, err
	}
	endBox(w) // mdat

	out := ws.Bytes()
	// Patch the single chunk offset to the real mdat payload position.
	patchChunkOffset(out, dataOffset)

	report, err := Verify(bytes.NewReader(out), int64(len(out)), cfg)
	if err != nil {
		return nil, MuxReport{}, err
	}
	return out, report, nil
}

func must(_ *gomp4.BoxInfo, err error) {
	if err != nil {
		panic(err)
	}
}

func startBox(w *gomp4.Writer, t gomp4.BoxType) {
	if _, err := w.StartBox(&gomp4.BoxInfo{Type: t}); err != nil {
		panic(err)
	}
}

func endBox(w *gomp4.Writer) {
	if _, err := w.EndBox(); err != nil {
		panic(err)
	}
}

// leaf writes a complete leaf box: header plus its marshalled payload.
func leaf(w *gomp4.Writer, t gomp4.BoxType, box gomp4.IImmutableBox) {
	startBox(w, t)
	leafPayload(w, box)
	endBox(w)
}

// leafPayload writes only a box's field payload inside an already-open box.
func leafPayload(w *gomp4.Writer, box gomp4.IImmutableBox) {
	if _, err := gomp4.Marshal(w, box, gomp4.Context{}); err != nil {
		panic(err)
	}
}

func boxType(s string) gomp4.BoxType {
	var bt gomp4.BoxType
	copy(bt[:], s)
	return bt
}

// patchChunkOffset rewrites the lone stco entry to the mdat payload offset.
// It parses the box header rather than assuming fixed field positions.
func patchChunkOffset(b []byte, offset uint32) {
	marker := []byte{'s', 't', 'c', 'o'}
	idx := bytes.Index(b, marker)
	if idx < 0 {
		panic("mp4: stco box not found")
	}
	boxStart := idx - 4 // marker follows the 4-byte size field
	size := binary.BigEndian.Uint32(b[boxStart:])
	headerSize := uint32(8)
	if size == 1 {
		headerSize = 16 // 64-bit largesize
	}
	// Payload: 4 version/flags, 4 entry count, then 4-byte chunk offsets.
	entryPos := boxStart + int(headerSize) + 4 + 4
	if entryPos+4 > len(b) {
		panic("mp4: stco entry out of bounds")
	}
	binary.BigEndian.PutUint32(b[entryPos:], offset)
}

// writeSeeker is a growable in-memory io.WriteSeeker. Unlike bytes.Buffer it
// supports seeking backward and overwriting earlier bytes (go-mp4 patches box
// sizes this way); length always tracks the furthest byte written.
type writeSeeker struct {
	data []byte
	pos  int64
}

func (w *writeSeeker) Write(p []byte) (int, error) {
	end := w.pos + int64(len(p))
	if end > int64(len(w.data)) {
		w.data = append(w.data, make([]byte, end-int64(len(w.data)))...)
	}
	copy(w.data[w.pos:end], p)
	w.pos = end
	return len(p), nil
}

func (w *writeSeeker) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = w.pos + offset
	case io.SeekEnd:
		abs = int64(len(w.data)) + offset
	}
	if abs < 0 {
		return 0, fmt.Errorf("negative seek")
	}
	w.pos = abs
	return abs, nil
}

func (w *writeSeeker) Len() int64 { return int64(len(w.data)) }
func (w *writeSeeker) Bytes() []byte { return w.data }
