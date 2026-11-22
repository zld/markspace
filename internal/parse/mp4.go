package parse

import (
	"bytes"
	"io"
	"math"
	"os"

	"archive/internal/model"

	mp4 "github.com/abema/go-mp4"
)

// ParseMP4File reads and parses one local MP4 file.
func ParseMP4File(path string) (*Track, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > model.MaxInputBytes {
		return nil, model.ParseFault(0, -1, "input exceeds 2 GiB limit")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseMP4(path, data)
}

// ParseMP4 parses the supported MP4 subset from memory. Every
// structural failure is a *model.Fault with the raw byte offset.
func ParseMP4(name string, data []byte) (*Track, error) {
	p := &mp4Parser{data: data, tr: &Track{Source: name, Format: "mp4", TrackID: 0}}
	if err := p.walk(0, int64(len(data))); err != nil {
		return nil, err
	}
	if err := p.buildSamples(); err != nil {
		return nil, err
	}
	if p.tr.TimeScale <= 0 {
		return nil, model.ParseFault(0, -1, "missing or invalid media timescale")
	}
	if p.tr.Width > model.MaxFrameDim || p.tr.Height > model.MaxFrameDim {
		return nil, model.ParseFault(0, -1, "frame dimensions exceed %d", model.MaxFrameDim)
	}
	if c := p.tr.Crop; c != nil {
		if c.X+c.W > p.tr.Width || c.Y+c.H > p.tr.Height || c.W == 0 || c.H == 0 {
			return nil, model.ParseFault(p.cropOff, -1,
				"malformed crop rectangle %dx%d+%d+%d exceeds frame %dx%d",
				c.W, c.H, c.X, c.Y, p.tr.Width, p.tr.Height)
		}
	}
	return p.tr, nil
}

type mp4Parser struct {
	data     []byte
	tr       *Track
	stts     *mp4.Stts
	stsz     *mp4.Stsz
	stsc     *mp4.Stsc
	stco     *mp4.Stco
	stss     *mp4.Stss
	sttsOff  int64
	stcoOff  int64 // offset of first ChunkOffset entry
	cropOff  int64
	sawStbl  bool
	inTrak   bool
	doneTrak bool
}

func (p *mp4Parser) walk(off, limit int64) error {
	r := bytes.NewReader(p.data)
	for off < limit {
		if _, err := r.Seek(off, io.SeekStart); err != nil {
			return model.ParseFault(off, -1, "seek failed: %v", err)
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			return model.ParseFault(off, -1, "malformed box header: %v", err)
		}
		if bi.Size < bi.HeaderSize || bi.Size > uint64(limit)-uint64(off) {
			return model.ParseFault(off, -1, "box %s size %d exceeds enclosing bounds", bi.Type, bi.Size)
		}
		body := int64(bi.Offset) + int64(bi.HeaderSize)
		end := int64(bi.Offset) + int64(bi.Size)
		switch {
		case mp4Containers[bi.Type]:
			if bi.Type == mp4.StrToBoxType("trak") {
				if p.doneTrak {
					return model.ParseFault(int64(bi.Offset), -1, "multiple tracks per file not supported")
				}
				p.doneTrak = true
			}
			if err := p.walk(body, end); err != nil {
				return err
			}
		default:
			if err := p.leaf(r, bi, body); err != nil {
				return err
			}
		}
		off = end
	}
	return nil
}

func unmarshalBox(r *bytes.Reader, at int64, size uint64, box mp4.IBox) error {
	if _, err := r.Seek(at, io.SeekStart); err != nil {
		return err
	}
	_, err := mp4.Unmarshal(r, size, box, mp4.Context{})
	return err
}

func (p *mp4Parser) leaf(r *bytes.Reader, bi *mp4.BoxInfo, body int64) error {
	off := int64(bi.Offset)
	payload := bi.Size - bi.HeaderSize
	factory, ok := mp4Leaf[bi.Type]
	if !ok {
		if mp4Skippable[bi.Type] {
			return nil
		}
		return model.ParseFault(off, -1, "unknown box type %q: stopping track", bi.Type.String())
	}
	box := factory()
	if err := unmarshalBox(r, body, payload, box); err != nil {
		return model.ParseFault(off, -1, "corrupt %s box: %v", bi.Type, err)
	}
	switch b := box.(type) {
	case *mp4.Tkhd:
		p.tr.TrackID = int(b.TrackID)
		p.tr.Matrix = b.Matrix
		p.tr.Width = b.Width >> 16
		p.tr.Height = b.Height >> 16
	case *mp4.Mdhd:
		p.tr.TimeScale = int64(b.Timescale)
	case *mp4.Elst:
		for _, e := range b.Entries {
			if mt := e.MediaTimeV0; mt >= 0 {
				p.tr.MediaOffset = int64(mt)
				break
			}
		}
	case *mp4.Stts:
		p.stts, p.sttsOff = b, off
	case *mp4.Stsz:
		p.stsz = b
	case *mp4.Stsc:
		p.stsc = b
	case *mp4.Stco:
		p.stco = b
		p.stcoOff = body + 8 // fullbox header + entry_count
	case *mp4.Stss:
		p.stss = b
	case *mp4.Colr:
		p.tr.Colors = append(p.tr.Colors, ColorInfo{
			Primaries: b.ColourPrimaries, Transfer: b.TransferCharacteristics, Matrix: b.MatrixCoefficients,
		})
	case *cropBox:
		p.tr.Crop = &Crop{X: b.X, Y: b.Y, W: b.W, H: b.H}
		p.cropOff = off
	case *rtpBox:
		ts, err := ParseRTPPacket(b.Payload[:], off, p.tr.TrackID)
		if err != nil {
			return err
		}
		p.tr.RTPTimestamp, p.tr.HasRTP = ts, true
	}
	return nil
}

// buildSamples joins stts/stsz/stsc/stco/stss into per-sample entries
// and validates that every payload range lies inside the input.
func (p *mp4Parser) buildSamples() error {
	if p.stts == nil || p.stsz == nil || p.stsc == nil || p.stco == nil {
		return model.ParseFault(0, -1, "incomplete sample table")
	}
	n := int(p.stsz.SampleCount)
	sizes := make([]int64, n)
	if p.stsz.SampleSize != 0 {
		for i := range sizes {
			sizes[i] = int64(p.stsz.SampleSize)
		}
	} else {
		if len(p.stsz.EntrySize) != n {
			return model.ParseFault(p.sttsOff, -1, "stsz entry count mismatch")
		}
		for i, s := range p.stsz.EntrySize {
			sizes[i] = int64(s)
		}
	}
	dts := make([]int64, n)
	durs := make([]int64, n)
	var cur int64
	idx := 0
	for _, e := range p.stts.Entries {
		for k := uint32(0); k < e.SampleCount && idx < n; k++ {
			dts[idx] = cur
			durs[idx] = int64(e.SampleDelta)
			if int64(e.SampleDelta) > math.MaxInt64-cur {
				return model.ParseFault(p.sttsOff, -1, "decode time overflow in stts")
			}
			cur += int64(e.SampleDelta)
			idx++
		}
	}
	if idx != n {
		return model.ParseFault(p.sttsOff, -1, "stts covers %d of %d samples", idx, n)
	}
	key := make([]bool, n)
	if p.stss == nil {
		for i := range key {
			key[i] = true
		}
	} else {
		for _, sn := range p.stss.SampleNumber {
			if sn == 0 || int(sn) > n {
				return model.ParseFault(p.sttsOff, -1, "stss sample number %d out of range", sn)
			}
			key[sn-1] = true
		}
	}
	// Chunk layout: stsc maps chunks to samples-per-chunk.
	entries := p.stsc.Entries
	spc := func(chunk uint32) uint32 {
		v := entries[0].SamplesPerChunk
		for _, e := range entries {
			if e.FirstChunk <= chunk {
				v = e.SamplesPerChunk
			}
		}
		return v
	}
	samples := make([]Sample, 0, n)
	si := 0
	for ci, co := range p.stco.ChunkOffset {
		chunkOff := int64(co)
		per := spc(uint32(ci) + 1)
		for s := uint32(0); s < per && si < n; s++ {
			if chunkOff < 0 || chunkOff+sizes[si] > int64(len(p.data)) {
				return model.ParseFault(p.stcoOff+4*int64(ci), -1,
					"sample %d payload [%d,+%d) exceeds input size %d", si, chunkOff, sizes[si], len(p.data))
			}
			samples = append(samples, Sample{
				Index: uint32(si), DTS: dts[si], Duration: durs[si],
				Offset: chunkOff, Size: uint32(sizes[si]), Keyframe: key[si],
			})
			chunkOff += sizes[si]
			si++
		}
	}
	if si != n {
		return model.ParseFault(p.stcoOff, -1, "chunk table covers %d of %d samples", si, n)
	}
	p.tr.Samples = samples
	return nil
}
