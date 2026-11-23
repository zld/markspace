// Package cmaf parses fragmented MP4/CMAF input into track runs with explicit
// box provenance.
//
// Boundaries are read with go-mp4's BoxInfo reader, payloads with go-mp4's
// unmarshaller. Every failure carries the fragment ordinal, box type and byte
// offset so a truncated input can be reported and skipped without committing
// partial output.
package cmaf

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"

	"avbatch/internal/mediaerr"
)

// Limits bound hostile input before any allocation proportional to a count
// field is made.
type Limits struct {
	MaxFragmentSize  int64
	MaxGroupSize     int64
	MaxSamplesPerRun uint64
	MaxBoxes         int
	MaxDepth         int
}

// DefaultLimits match the product constraints: 64 MiB per fragment, 2 GiB per
// group.
func DefaultLimits() Limits {
	return Limits{
		MaxFragmentSize:  64 << 20,
		MaxGroupSize:     2 << 30,
		MaxSamplesPerRun: 1 << 20,
		MaxBoxes:         1 << 16,
		MaxDepth:         24,
	}
}

// BoxSpan is one box boundary as read from the input.
type BoxSpan struct {
	Type       string
	Offset     int64
	Size       int64
	HeaderSize int64
}

func (b BoxSpan) payloadStart() int64 { return b.Offset + b.HeaderSize }
func (b BoxSpan) payloadSize() int64  { return b.Size - b.HeaderSize }

// TrackInfo is what the init segment declares for one track.
type TrackInfo struct {
	ID        uint32
	Timescale uint32
	Handler   string
}

// Init is the parsed initialization segment.
type Init struct {
	MovieTimescale uint32
	Tracks         map[uint32]TrackInfo
	TrackOrder     []uint32
	Digest         [32]byte
}

// Sample is one coded sample with its absolute offset inside the fragment.
type Sample struct {
	Index             int
	Duration          uint32
	Size              uint32
	Flags             uint32
	CompositionOffset int32
	Offset            int64
}

// TrackRun is one trun within a traf.
type TrackRun struct {
	TrackID        uint32
	BaseDecodeTime uint64
	Timescale      uint32
	Handler        string
	Samples        []Sample
	TotalSize      int64
	// Repaired is set when a tfdt regression was corrected downstream.
	Repaired bool
}

// Fragment is one moof+mdat pair.
type Fragment struct {
	Ordinal     int
	Size        int64
	Sequence    uint32
	HasSequence bool
	MoofOffset  int64
	MoofSize    int64
	Runs        []TrackRun
	Boxes       []BoxSpan
	Digest      [32]byte
}

// VideoRun returns the first video run, or nil.
func (f *Fragment) VideoRun() *TrackRun { return f.runWithHandler("vide") }

// AudioRun returns the first audio run, or nil.
func (f *Fragment) AudioRun() *TrackRun { return f.runWithHandler("soun") }

func (f *Fragment) runWithHandler(h string) *TrackRun {
	for i := range f.Runs {
		if f.Runs[i].Handler == h {
			return &f.Runs[i]
		}
	}
	return nil
}

var containerTypes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true,
	"moof": true, "traf": true, "mvex": true, "edts": true, "dinf": true,
	"udta": true, "mfra": true, "stsd": true, "sinf": true, "schi": true,
	"wave": true,
}

// marshaler limits how deep scanLevel recurses; passed through parse state.
type scanner struct {
	data   []byte
	limits Limits
	count  int
	flat   []BoxSpan
}

func (s *scanner) level(off, end int64, depth int) ([]BoxSpan, error) {
	if depth > s.limits.MaxDepth {
		return nil, mediaerr.New(mediaerr.KindInvalidBox, -1, "", off, "box nesting too deep")
	}
	var spans []BoxSpan
	for off < end {
		if end-off < 8 {
			return spans, mediaerr.New(mediaerr.KindTruncatedBox, -1, "", off,
				fmt.Sprintf("%d trailing bytes cannot hold a box header", end-off))
		}
		span, err := s.readSpan(off, end)
		if err != nil {
			return spans, err
		}
		s.count++
		if s.count > s.limits.MaxBoxes {
			return spans, mediaerr.New(mediaerr.KindInvalidBox, -1, span.Type, span.Offset, "too many boxes")
		}
		s.flat = append(s.flat, span)
		spans = append(spans, span)
		if containerTypes[span.Type] {
			if _, err := s.level(span.payloadStart(), span.Offset+span.Size, depth+1); err != nil {
				return spans, err
			}
		}
		off = span.Offset + span.Size
	}
	return spans, nil
}

func (s *scanner) readSpan(off, end int64) (BoxSpan, error) {
	r := bytes.NewReader(s.data)
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return BoxSpan{}, mediaerr.Wrap(mediaerr.KindTruncatedBox, -1, "", off, "seek failed", err)
	}
	bi, err := mp4.ReadBoxInfo(r)
	if err != nil {
		kind := mediaerr.KindInvalidBoxSize
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			kind = mediaerr.KindTruncatedBox
		}
		return BoxSpan{}, mediaerr.Wrap(kind, -1, "", off, "unreadable box header", err)
	}
	size := int64(bi.Size)
	if bi.ExtendToEOF {
		size = end - off
	}
	header := int64(bi.HeaderSize)
	span := BoxSpan{
		Type:       bi.Type.String(),
		Offset:     off,
		Size:       size,
		HeaderSize: header,
	}
	switch {
	case size < header || size < 8:
		return span, mediaerr.New(mediaerr.KindInvalidBoxSize, -1, span.Type, off,
			fmt.Sprintf("box size %d smaller than header %d", size, header))
	case size > end-off:
		return span, mediaerr.New(mediaerr.KindTruncatedBox, -1, span.Type, off,
			fmt.Sprintf("box declares %d bytes but only %d remain", size, end-off))
	}
	return span, nil
}

func (s *scanner) children(parent BoxSpan, depth int) ([]BoxSpan, error) {
	return s.level(parent.payloadStart(), parent.Offset+parent.Size, depth)
}

func topLevel(data []byte, limits Limits) ([]BoxSpan, []BoxSpan, error) {
	s := &scanner{data: data, limits: limits}
	spans, err := s.level(0, int64(len(data)), 0)
	return spans, s.flat, err
}

// ParseInit parses ftyp+moov and returns per-track timebases.
func ParseInit(data []byte, limits Limits) (*Init, error) {
	if int64(len(data)) > limits.MaxFragmentSize {
		return nil, mediaerr.New(mediaerr.KindInvalidBox, -1, "", 0, "init segment exceeds size limit")
	}
	spans, flat, err := topLevel(data, limits)
	if err != nil {
		return nil, err
	}
	init := &Init{Tracks: map[uint32]TrackInfo{}, Digest: sha256.Sum256(data)}
	var moov *BoxSpan
	haveFtyp := false
	for i := range spans {
		switch spans[i].Type {
		case "ftyp":
			haveFtyp = true
		case "moov":
			if moov != nil {
				return nil, mediaerr.New(mediaerr.KindInvalidBox, -1, "moov", spans[i].Offset, "duplicate moov")
			}
			moov = &spans[i]
		}
	}
	if !haveFtyp {
		return nil, mediaerr.New(mediaerr.KindMissingField, -1, "ftyp", 0, "init segment has no ftyp")
	}
	if moov == nil {
		return nil, mediaerr.New(mediaerr.KindMissingField, -1, "moov", 0, "init segment has no moov")
	}
	s := &scanner{data: data, limits: limits, flat: flat}
	kids, err := s.children(*moov, 1)
	if err != nil {
		return nil, err
	}
	var mvhd *mp4.Mvhd
	var traks []BoxSpan
	for _, k := range kids {
		switch k.Type {
		case "mvhd":
			mvhd = &mp4.Mvhd{}
			if err := unmarshalBox(data, k, mvhd); err != nil {
				return nil, err
			}
		case "trak":
			traks = append(traks, k)
		}
	}
	if mvhd == nil {
		return nil, mediaerr.New(mediaerr.KindMissingField, -1, "mvhd", moov.Offset, "moov has no mvhd")
	}
	init.MovieTimescale = mvhd.Timescale
	if init.MovieTimescale == 0 {
		return nil, mediaerr.New(mediaerr.KindMissingField, -1, "mvhd", mvhdOffset(data, moov, kids), "mvhd timescale is zero")
	}
	for _, trak := range traks {
		info, err := parseTrak(data, limits, trak)
		if err != nil {
			return nil, err
		}
		if _, dup := init.Tracks[info.ID]; dup {
			return nil, mediaerr.New(mediaerr.KindInvalidBox, -1, "tkhd", trak.Offset,
				fmt.Sprintf("duplicate track id %d", info.ID))
		}
		init.Tracks[info.ID] = info
		init.TrackOrder = append(init.TrackOrder, info.ID)
	}
	if len(init.Tracks) == 0 {
		return nil, mediaerr.New(mediaerr.KindMissingField, -1, "trak", moov.Offset, "moov has no tracks")
	}
	return init, nil
}

func mvhdOffset(data []byte, moov *BoxSpan, kids []BoxSpan) int64 {
	for _, k := range kids {
		if k.Type == "mvhd" {
			return k.Offset
		}
	}
	return moov.Offset
}

func parseTrak(data []byte, limits Limits, trak BoxSpan) (TrackInfo, error) {
	s := &scanner{data: data, limits: limits}
	kids, err := s.children(trak, 2)
	if err != nil {
		return TrackInfo{}, err
	}
	var info TrackInfo
	var haveTkhd, haveMdhd, haveHdlr bool
	for _, k := range kids {
		switch k.Type {
		case "tkhd":
			box := &mp4.Tkhd{}
			if err := unmarshalBox(data, k, box); err != nil {
				return TrackInfo{}, err
			}
			info.ID = box.TrackID
			haveTkhd = true
		case "mdia":
			mdiaKids, err := s.children(k, 3)
			if err != nil {
				return TrackInfo{}, err
			}
			for _, m := range mdiaKids {
				switch m.Type {
				case "mdhd":
					box := &mp4.Mdhd{}
					if err := unmarshalBox(data, m, box); err != nil {
						return TrackInfo{}, err
					}
					info.Timescale = box.Timescale
					haveMdhd = true
				case "hdlr":
					box := &mp4.Hdlr{}
					if err := unmarshalBox(data, m, box); err != nil {
						return TrackInfo{}, err
					}
					info.Handler = string(box.HandlerType[:])
					haveHdlr = true
				}
			}
		}
	}
	switch {
	case !haveTkhd:
		return info, mediaerr.New(mediaerr.KindMissingField, -1, "tkhd", trak.Offset, "trak has no tkhd")
	case !haveMdhd:
		return info, mediaerr.New(mediaerr.KindMissingField, -1, "mdhd", trak.Offset, "trak has no mdhd")
	case !haveHdlr:
		return info, mediaerr.New(mediaerr.KindMissingField, -1, "hdlr", trak.Offset, "trak has no hdlr")
	case info.Timescale == 0:
		return info, mediaerr.New(mediaerr.KindMissingField, -1, "mdhd", trak.Offset, "mdhd timescale is zero")
	}
	return info, nil
}

// ParseFragment parses one moof+mdat fragment. ordinal is the zero-based
// position in the captured group and is attached to every error.
func ParseFragment(ordinal int, data []byte, init *Init, limits Limits) (*Fragment, error) {
	if int64(len(data)) > limits.MaxFragmentSize {
		return nil, mediaerr.New(mediaerr.KindInvalidBox, ordinal, "", 0,
			fmt.Sprintf("fragment is %d bytes, limit is %d", len(data), limits.MaxFragmentSize))
	}
	spans, flat, err := topLevel(data, limits)
	if err != nil {
		return nil, withOrdinal(err, ordinal)
	}
	frag := &Fragment{
		Ordinal: ordinal,
		Size:    int64(len(data)),
		Boxes:   flat,
		Digest:  sha256.Sum256(data),
	}
	var moof *BoxSpan
	haveMdat := false
	for i := range spans {
		switch spans[i].Type {
		case "moof":
			if moof != nil {
				return nil, mediaerr.New(mediaerr.KindInvalidBox, ordinal, "moof", spans[i].Offset,
					"fragment has multiple moof boxes")
			}
			moof = &spans[i]
		case "mdat":
			haveMdat = true
		}
	}
	if moof == nil {
		return nil, mediaerr.New(mediaerr.KindMissingField, ordinal, "moof", 0, "fragment has no moof")
	}
	frag.MoofOffset, frag.MoofSize = moof.Offset, moof.Size
	s := &scanner{data: data, limits: limits}
	kids, err := s.children(*moof, 1)
	if err != nil {
		return nil, withOrdinal(err, ordinal)
	}
	for _, k := range kids {
		switch k.Type {
		case "mfhd":
			box := &mp4.Mfhd{}
			if err := unmarshalBoxAt(data, k, box, ordinal); err != nil {
				return nil, err
			}
			frag.Sequence, frag.HasSequence = box.SequenceNumber, true
		case "traf":
			if err := parseTraf(data, limits, ordinal, k, init, frag); err != nil {
				return nil, err
			}
		}
	}
	if !frag.HasSequence {
		return nil, mediaerr.New(mediaerr.KindMissingField, ordinal, "mfhd", moof.Offset, "moof has no mfhd")
	}
	if len(frag.Runs) == 0 {
		return nil, mediaerr.New(mediaerr.KindMissingField, ordinal, "traf", moof.Offset, "moof has no traf")
	}
	if !haveMdat {
		for _, run := range frag.Runs {
			if len(run.Samples) > 0 {
				return nil, mediaerr.New(mediaerr.KindMissingField, ordinal, "mdat", moof.Offset,
					"fragment has samples but no mdat")
			}
		}
	}
	return frag, nil
}

func withOrdinal(err error, ordinal int) error {
	var me *mediaerr.Error
	if errors.As(err, &me) {
		c := *me
		c.Segment = ordinal
		return &c
	}
	return mediaerr.Wrap(mediaerr.KindInvalidBox, ordinal, "", 0, "parse failure", err)
}

func parseTraf(data []byte, limits Limits, ordinal int, traf BoxSpan, init *Init, frag *Fragment) error {
	s := &scanner{data: data, limits: limits}
	kids, err := s.children(traf, 2)
	if err != nil {
		return withOrdinal(err, ordinal)
	}
	var tfhd *mp4.Tfhd
	var tfdt *mp4.Tfdt
	var truns []BoxSpan
	for _, k := range kids {
		switch k.Type {
		case "tfhd":
			tfhd = &mp4.Tfhd{}
			if err := unmarshalBoxAt(data, k, tfhd, ordinal); err != nil {
				return err
			}
		case "tfdt":
			tfdt = &mp4.Tfdt{}
			if err := unmarshalBoxAt(data, k, tfdt, ordinal); err != nil {
				return err
			}
		case "trun":
			truns = append(truns, k)
		}
	}
	if tfhd == nil {
		return mediaerr.New(mediaerr.KindMissingField, ordinal, "tfhd", traf.Offset, "traf has no tfhd")
	}
	if tfdt == nil {
		return mediaerr.New(mediaerr.KindMissingField, ordinal, "tfdt", traf.Offset,
			fmt.Sprintf("traf for track %d has no tfdt", tfhd.TrackID))
	}
	if len(truns) == 0 {
		return mediaerr.New(mediaerr.KindMissingField, ordinal, "trun", traf.Offset,
			fmt.Sprintf("traf for track %d has no trun", tfhd.TrackID))
	}
	info, ok := init.Tracks[tfhd.TrackID]
	if !ok {
		return mediaerr.New(mediaerr.KindMissingField, ordinal, "tfhd", traf.Offset,
			fmt.Sprintf("track %d is not declared by the init segment", tfhd.TrackID))
	}
	run := TrackRun{
		TrackID:        tfhd.TrackID,
		BaseDecodeTime: tfdt.GetBaseMediaDecodeTime(),
		Timescale:      info.Timescale,
		Handler:        info.Handler,
	}
	base := frag.MoofOffset
	if tfhd.GetFlags()&mp4.TfhdBaseDataOffsetPresent != 0 {
		base = int64(tfhd.BaseDataOffset)
	}
	for _, trunSpan := range truns {
		trun, err := decodeTrun(data, trunSpan, ordinal, limits)
		if err != nil {
			return err
		}
		cursor := frag.MoofOffset + frag.MoofSize
		if trun.GetFlags()&0x1 != 0 {
			cursor = base + int64(trun.DataOffset)
		}
		for i := range trun.Entries {
			e := &trun.Entries[i]
			size := e.SampleSize
			if trun.GetFlags()&0x200 == 0 {
				size = tfhd.DefaultSampleSize
			}
			dur := e.SampleDuration
			if trun.GetFlags()&0x100 == 0 {
				dur = tfhd.DefaultSampleDuration
			}
			if size == 0 {
				return mediaerr.New(mediaerr.KindMissingField, ordinal, "trun", trunSpan.Offset,
					fmt.Sprintf("sample %d has no size", i))
			}
			if dur == 0 {
				return mediaerr.New(mediaerr.KindMissingField, ordinal, "trun", trunSpan.Offset,
					fmt.Sprintf("sample %d has no duration", i))
			}
			if cursor < 0 || cursor+int64(size) > int64(len(data)) {
				return mediaerr.New(mediaerr.KindTruncatedBox, ordinal, "mdat", cursor,
					fmt.Sprintf("sample %d needs %d bytes at offset %d but fragment is %d bytes", i, size, cursor, len(data)))
			}
			run.Samples = append(run.Samples, Sample{
				Index:             len(run.Samples),
				Duration:          dur,
				Size:              size,
				Flags:             e.SampleFlags,
				CompositionOffset: int32(trun.GetSampleCompositionTimeOffset(i)),
				Offset:            cursor,
			})
			run.TotalSize += int64(size)
			cursor += int64(size)
		}
	}
	frag.Runs = append(frag.Runs, run)
	return nil
}

func decodeTrun(data []byte, span BoxSpan, ordinal int, limits Limits) (*mp4.Trun, error) {
	if span.payloadSize() < 8 {
		return nil, mediaerr.New(mediaerr.KindTruncatedBox, ordinal, "trun", span.Offset,
			"trun payload too short for version, flags and sample count")
	}
	p := span.payloadStart()
	flags := uint32(data[p+1])<<16 | uint32(data[p+2])<<8 | uint32(data[p+3])
	count := binary.BigEndian.Uint32(data[p+4 : p+8])
	if uint64(count) > limits.MaxSamplesPerRun {
		return nil, mediaerr.New(mediaerr.KindInvalidBox, ordinal, "trun", span.Offset,
			fmt.Sprintf("sample count %d exceeds limit %d", count, limits.MaxSamplesPerRun))
	}
	entryBytes := int64(0)
	for _, bit := range []uint32{0x100, 0x200, 0x400, 0x800} {
		if flags&bit != 0 {
			entryBytes += 4
		}
	}
	optional := int64(0)
	if flags&0x1 != 0 {
		optional += 4
	}
	if flags&0x4 != 0 {
		optional += 4
	}
	need := 8 + optional + int64(count)*entryBytes
	if need > span.payloadSize() {
		return nil, mediaerr.New(mediaerr.KindTruncatedBox, ordinal, "trun", span.Offset,
			fmt.Sprintf("trun declares %d samples needing %d bytes but only %d are present", count, need, span.payloadSize()))
	}
	box := &mp4.Trun{}
	if err := unmarshalBoxAt(data, span, box, ordinal); err != nil {
		return nil, err
	}
	return box, nil
}

func unmarshalBox(data []byte, span BoxSpan, dst mp4.IBox) error {
	return unmarshalBoxAt(data, span, dst, -1)
}

func unmarshalBoxAt(data []byte, span BoxSpan, dst mp4.IBox, ordinal int) error {
	end := span.Offset + span.Size
	if end > int64(len(data)) {
		return mediaerr.New(mediaerr.KindTruncatedBox, ordinal, span.Type, span.Offset, "box extends past input")
	}
	// go-mp4 structs embed version+flags as the FullBox field, so the
	// unmarshaller wants them: decode from the start of the box payload.
	start := span.payloadStart()
	body := span.payloadSize()
	r := bytes.NewReader(data[:end])
	if _, err := r.Seek(start, io.SeekStart); err != nil {
		return mediaerr.Wrap(mediaerr.KindInvalidBox, ordinal, span.Type, span.Offset, "seek failed", err)
	}
	if _, err := mp4.Unmarshal(r, uint64(body), dst, mp4.Context{}); err != nil {
		return mediaerr.Wrap(mediaerr.KindMissingField, ordinal, span.Type, span.Offset,
			"payload could not be decoded", err)
	}
	return nil
}
