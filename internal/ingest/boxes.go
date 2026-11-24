// Package ingest parses CMAF/fMP4 fragment files into unified timeline
// sample records, enforcing hard input limits and producing locatable
// errors that carry the original byte offset.
package ingest

import (
	"bytes"
	"encoding/binary"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediamerge/internal/model"
)

// Hard input limits. Parsing never reads beyond these bounds.
const (
	MaxBoxBytes   = 16 << 20 // 16 MiB per box
	MaxTotalBytes = 64 << 20 // 64 MiB total input
)

// ColorMeta is the parsed nclx colour metadata of a video track.
type ColorMeta struct {
	Primaries uint16 `json:"primaries"`
	Transfer  uint16 `json:"transfer"`
	Matrix    uint16 `json:"matrix"`
	FullRange bool   `json:"full_range"`
}

// TrackMeta is the per-track metadata extracted from the init segment.
type TrackMeta struct {
	ID         uint32     `json:"id"`
	Handler    string     `json:"handler"` // "vide" or "soun"
	Timescale  uint32     `json:"timescale"`
	Channels   uint16     `json:"channels,omitempty"`
	SampleRate uint32     `json:"sample_rate,omitempty"`
	Width      uint32     `json:"width,omitempty"`
	Height     uint32     `json:"height,omitempty"`
	Color      *ColorMeta `json:"color,omitempty"`
	SourceFile string     `json:"source_file"`
}

// RawSample is one sample inside a fragment, still in track timescale.
type RawSample struct {
	Offset        int64 // absolute byte offset of payload in the source file
	Size          uint32
	DurationTicks uint32
	Index         uint32
}

// Fragment is one parsed moof (one traf) with its samples.
type Fragment struct {
	Seq        uint32
	TrackID    uint32
	TFDT       uint64
	HasTFDT    bool
	MoofOffset int64
	Samples    []RawSample
}

// File is the parse result for one input file. Data is retained (bounded by
// MaxTotalBytes) so later stages can slice sample payloads without re-reading.
type File struct {
	Path   string
	Data   []byte
	Tracks []TrackMeta
	Frags  []Fragment
}

// Meta returns the metadata for track id, or nil.
func (f *File) Meta(id uint32) *TrackMeta {
	for i := range f.Tracks {
		if f.Tracks[i].ID == id {
			return &f.Tracks[i]
		}
	}
	return nil
}

type parser struct {
	path string
	data []byte
	errs *model.Errors
	file *File

	fatal        *model.Error
	truncatedBox *model.Error

	curMoofOff  int64
	curSeq      uint32
	curTrackID  uint32
	curTFDT     uint64
	curHasTFDT  bool
	lastFragTrk uint32
	curMeta     *TrackMeta // track whose moov subtree is being walked
}

// ParseFile parses one fMP4 file. Structural problems are reported through
// errs; a fatal structural error is also returned directly.
func ParseFile(path string, data []byte, errs *model.Errors) (*File, *model.Error) {
	p := &parser{
		path: path,
		data: data,
		errs: errs,
		file: &File{Path: path, Data: data},
	}
	p.walk(bytes.NewReader(data), int64(len(data)), 0)
	if p.fatal != nil {
		errs.Add(p.fatal)
		return p.file, p.fatal
	}
	if p.truncatedBox != nil {
		// The track owning the truncated box is quarantined; every other
		// track remains usable.
		tk := p.lastFragTrk
		p.truncatedBox.TrackID = tk
		errs.Add(p.truncatedBox)
		kept := p.file.Frags[:0]
		for _, fr := range p.file.Frags {
			if fr.TrackID != tk {
				kept = append(kept, fr)
			}
		}
		p.file.Frags = kept
	}
	return p.file, nil
}

func (p *parser) walk(r *bytes.Reader, end int64, depth int) {
	for p.fatal == nil && p.truncatedBox == nil {
		pos, err := r.Seek(0, io.SeekCurrent)
		if err != nil || pos >= end {
			return
		}
		bi, err := mp4.ReadBoxInfo(r)
		if err != nil {
			p.truncateErr(pos, "box header incomplete")
			return
		}
		if bi.Size > MaxBoxBytes {
			p.truncateErr(int64(bi.Offset), "box exceeds 16 MiB limit")
			return
		}
		if bi.Size < bi.HeaderSize {
			p.truncateErr(int64(bi.Offset), "invalid box size")
			return
		}
		payloadEnd := int64(bi.Offset) + int64(bi.Size)
		if payloadEnd > end {
			p.truncateErr(int64(bi.Offset), "truncated box: declared size exceeds available data")
			return
		}
		p.dispatch(r, bi, payloadEnd, depth)
		if _, err := r.Seek(payloadEnd, io.SeekStart); err != nil {
			p.truncateErr(payloadEnd, "seek failed")
			return
		}
	}
}

func (p *parser) dispatch(r *bytes.Reader, bi *mp4.BoxInfo, payloadEnd int64, depth int) {
	switch bi.Type {
	case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeDinf():
		p.walk(r, payloadEnd, depth+1)
	case mp4.BoxTypeMoof():
		p.curMoofOff = int64(bi.Offset)
		p.walk(r, payloadEnd, depth+1)
	case mp4.BoxTypeTraf():
		p.curTrackID, p.curHasTFDT = 0, false
		p.walk(r, payloadEnd, depth+1)
	case mp4.BoxTypeMfhd():
		var b mp4.Mfhd
		if p.unmarshal(r, bi, &b) {
			p.curSeq = b.SequenceNumber
		}
	case mp4.BoxTypeTfhd():
		var b mp4.Tfhd
		if p.unmarshal(r, bi, &b) {
			p.curTrackID = b.TrackID
		}
	case mp4.BoxTypeTfdt():
		var b mp4.Tfdt
		if p.unmarshal(r, bi, &b) {
			p.curTFDT = b.BaseMediaDecodeTimeV1
			if b.GetVersion() == 0 {
				p.curTFDT = uint64(b.BaseMediaDecodeTimeV0)
			}
			p.curHasTFDT = true
		}
	case mp4.BoxTypeTrun():
		var b mp4.Trun
		if p.unmarshal(r, bi, &b) {
			p.addFragment(&b)
		}
	case mp4.BoxTypeTkhd():
		var b mp4.Tkhd
		if p.unmarshal(r, bi, &b) {
			p.file.Tracks = append(p.file.Tracks, TrackMeta{ID: b.TrackID, SourceFile: p.path})
			p.curMeta = &p.file.Tracks[len(p.file.Tracks)-1]
		}
	case mp4.BoxTypeMdhd():
		var b mp4.Mdhd
		if p.unmarshal(r, bi, &b) && p.curMeta != nil {
			p.curMeta.Timescale = b.Timescale
		}
	case mp4.BoxTypeHdlr():
		var b mp4.Hdlr
		if p.unmarshal(r, bi, &b) && p.curMeta != nil {
			p.curMeta.Handler = string(b.HandlerType[:])
		}
	case mp4.BoxTypeStsd():
		p.parseStsd(r, bi)
	case mp4.BoxTypeMdat():
		// Payload is referenced by trun offsets; nothing to parse here.
	default:
		// Unknown boxes are skipped; their bytes stay untouched.
	}
}

// unmarshal reads one box payload through go-mp4 with strict bounds.
func (p *parser) unmarshal(r *bytes.Reader, bi *mp4.BoxInfo, dst mp4.IBox) bool {
	size := bi.Size - bi.HeaderSize
	if size > MaxBoxBytes {
		p.truncateErr(int64(bi.Offset), "box payload exceeds limit")
		return false
	}
	if _, err := mp4.Unmarshal(r, size, dst, mp4.Context{}); err != nil {
		p.truncateErr(int64(bi.Offset), "box payload unreadable: "+err.Error())
		return false
	}
	return true
}

// addFragment converts a parsed trun into a Fragment with absolute sample
// offsets (default-base-is-moof addressing).
func (p *parser) addFragment(trun *mp4.Trun) {
	flags := trun.GetFlags()
	frag := Fragment{
		Seq:        p.curSeq,
		TrackID:    p.curTrackID,
		TFDT:       p.curTFDT,
		HasTFDT:    p.curHasTFDT,
		MoofOffset: p.curMoofOff,
	}
	var base int64
	if flags&0x000001 != 0 {
		base = p.curMoofOff + int64(trun.DataOffset)
	} else {
		p.errs.Add(model.NewError(model.LevelRecovered, "ingest", p.path,
			p.curMoofOff, p.curTrackID, "trun without data-offset; samples skipped"))
		p.file.Frags = append(p.file.Frags, frag)
		return
	}
	off := base
	p.lastFragTrk = frag.TrackID // own truncation diagnostics even mid-fragment
	for i, e := range trun.Entries {
		sz := e.SampleSize
		dur := e.SampleDuration
		if off < 0 || off+int64(sz) > int64(len(p.data)) {
			p.truncateErr(off, "sample payload extends past end of input")
			return
		}
		frag.Samples = append(frag.Samples, RawSample{
			Offset: off, Size: sz, DurationTicks: dur, Index: uint32(i),
		})
		off += int64(sz)
	}
	p.file.Frags = append(p.file.Frags, frag)
}

// parseStsd extracts audio channel/rate and video dimension/colour fields
// from the sample description entry.
func (p *parser) parseStsd(r *bytes.Reader, bi *mp4.BoxInfo) {
	if p.curMeta == nil {
		return
	}
	size := bi.Size - bi.HeaderSize
	buf := make([]byte, size)
	if _, err := r.Read(buf); err != nil || len(buf) < 8 {
		p.truncateErr(int64(bi.Offset), "stsd payload unreadable")
		return
	}
	entry := buf[8:] // skip version/flags + entry count
	for len(entry) >= 8 {
		esz := binary.BigEndian.Uint32(entry[0:4])
		if esz < 8 || int(esz) > len(entry) {
			p.truncateErr(int64(bi.Offset), "stsd entry truncated")
			return
		}
		typ := string(entry[4:8])
		body := entry[8:esz]
		switch typ {
		case "mp4a":
			if len(body) >= 28 {
				p.curMeta.Channels = binary.BigEndian.Uint16(body[16:18])
				p.curMeta.SampleRate = binary.BigEndian.Uint32(body[24:28]) >> 16
			}
		case "avc1":
			if len(body) >= 78 {
				p.curMeta.Width = uint32(binary.BigEndian.Uint16(body[24:26]))
				p.curMeta.Height = uint32(binary.BigEndian.Uint16(body[26:28]))
				p.parseAvc1Children(body[78:])
			}
		}
		entry = entry[esz:]
	}
}

func (p *parser) parseAvc1Children(children []byte) {
	for len(children) >= 8 {
		csz := binary.BigEndian.Uint32(children[0:4])
		if csz < 8 || int(csz) > len(children) {
			return
		}
		if string(children[4:8]) == "colr" && csz >= 8+11 && string(children[8:12]) == "nclx" {
			p.curMeta.Color = &ColorMeta{
				Primaries: binary.BigEndian.Uint16(children[12:14]),
				Transfer:  binary.BigEndian.Uint16(children[14:16]),
				Matrix:    binary.BigEndian.Uint16(children[16:18]),
				FullRange: children[18]&0x80 != 0,
			}
		}
		children = children[csz:]
	}
}

// truncateErr records a locatable truncation. Truncation inside the init
// segment is fatal; truncation in the fragment stream isolates the track.
func (p *parser) truncateErr(off int64, msg string) {
	if p.curMoofOff == 0 && p.lastFragTrk == 0 {
		p.fatal = model.NewError(model.LevelFatal, "ingest", p.path, off, 0, "init segment: %s", msg)
		return
	}
	p.truncatedBox = model.NewError(model.LevelIsolated, "ingest", p.path, off, 0, "%s", msg)
}
