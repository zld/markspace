package continuity

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
	"github.com/pion/rtp"
)

// Demuxing is restricted to fragmented MP4/CMAF init + media segments. Every
// sample keeps its compressed bytes, its codec configuration and its integer
// timing; nothing is decoded or re-encoded.

// Codec identifies a compressed sample entry.
type Codec string

const (
	CodecAVC  Codec = "avc1"
	CodecHEVC Codec = "hvc1"
	CodecAAC  Codec = "mp4a"
)

// Track is a stable description of a demuxed track. The key is independent of
// the source track_ID so primary/backup switchover keeps one target track.
type Track struct {
	Key         string
	SourceID    uint32
	Kind        MediaKind
	Handler     string
	Timescale   uint32
	Codec       Codec
	SampleEntry string
	// CodecConfig is the raw decoder configuration box payload (avcC/hvcC) or
	// the AAC AudioSpecificConfig bytes (esds DecoderSpecificInfo).
	CodecConfig  []byte
	Width        uint16
	Height       uint16
	ChannelCount uint16
	SampleRate   uint32
	defaults     trexDefaults
}

// trexDefaults carries per-track fragment defaults from the init segment.
type trexDefaults struct {
	duration uint32
	size     uint32
}

// Sample is one compressed media sample with integer timing.
type Sample struct {
	TrackKey   string
	SourceID   uint32
	Segment    string
	File       string
	Index      int
	DTS        Ticks
	PTS        Ticks
	Duration   Ticks
	Sync       bool
	ByteOffset int64 // offset of the sample payload within its segment file
	ByteLength int64
	Data       []byte
	// SourceDTS and SourceDuration are in the track timescale, kept for
	// bit-exact remuxing.
	SourceDTS      uint64
	SourceDuration uint32
	SourceCto      int32
}

// End returns the exclusive end of the sample on the presentation timeline.
func (s Sample) End() Ticks { return s.PTS + s.Duration }

// InitSegment is the demuxed initialization data.
type InitSegment struct {
	Tracks []Track
	// Ftyp is the raw ftyp payload, preserved for remuxing.
	Ftyp []byte
}

// Segment is the demuxed result of one media segment file.
type Segment struct {
	File    string
	Samples []Sample
	// TrackID is the track the fragment belongs to.
	TrackID uint32
	// BaseDecodeTime is the tfdt value in the track timescale.
	BaseDecodeTime uint64
	// Timescale is the track timescale.
	Timescale uint32
}

const (
	// maxSegmentBytes bounds a single segment read.
	maxSegmentBytes = 64 << 20
	// maxSamplesPerTrack bounds demuxed output per segment.
	maxSamplesPerTrack = 100000
)

// ParseInit demuxes an init segment (ftyp+moov). name is used for fault
// positions and must be the manifest-relative path.
func ParseInit(name string, data []byte) (*InitSegment, error) {
	if len(data) > maxSegmentBytes {
		return nil, NewError(KindParse, locWithFile(name, 0), "init segment is %d bytes, limit %d", len(data), maxSegmentBytes)
	}
	r := bytes.NewReader(data)
	init := &InitSegment{}
	seenMoov := false
	_, err := mp4.ReadBoxStructure(r, func(h *mp4.ReadHandle) (any, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeFtyp():
			if init.Ftyp != nil {
				return nil, NewError(KindParse, locWithFile(name, int64(h.BoxInfo.Offset)), "duplicate ftyp")
			}
			payload := make([]byte, h.BoxInfo.Size-h.BoxInfo.HeaderSize)
			if _, err := r.Seek(int64(h.BoxInfo.Offset+h.BoxInfo.HeaderSize), io.SeekStart); err != nil {
				return nil, err
			}
			if _, err := io.ReadFull(r, payload); err != nil {
				return nil, WrapError(err, KindTruncated, locWithFile(name, int64(h.BoxInfo.Offset)), "short ftyp")
			}
			init.Ftyp = payload
			return nil, nil
		case mp4.BoxTypeMoov():
			if seenMoov {
				return nil, NewError(KindParse, locWithFile(name, int64(h.BoxInfo.Offset)), "duplicate moov")
			}
			seenMoov = true
			if err := parseMoov(name, data, h, init); err != nil {
				return nil, err
			}
			return nil, nil
		case mp4.BoxTypeMoof(), mp4.BoxTypeMdat(), mp4.BoxTypeStyp(), mp4.BoxTypeSidx():
			return nil, NewError(KindParse, locWithFile(name, int64(h.BoxInfo.Offset)), "unexpected %s in an init segment", h.BoxInfo.Type.String())
		}
		return nil, nil
	})
	if err != nil {
		var ce *Error
		if errors.As(err, &ce) {
			return nil, err
		}
		return nil, WrapError(err, KindParse, locWithFile(name, 0), "init segment box scan failed")
	}
	if init.Ftyp == nil {
		return nil, NewError(KindParse, locWithFile(name, 0), "missing ftyp")
	}
	if !seenMoov {
		return nil, NewError(KindParse, locWithFile(name, int64(len(data))), "missing moov")
	}
	if len(init.Tracks) == 0 {
		return nil, NewError(KindParse, locWithFile(name, 0), "moov declares no tracks")
	}
	return init, nil
}

func parseMoov(name string, data []byte, h *mp4.ReadHandle, init *InitSegment) error {
	mvhdBoxes, err := mp4.ExtractBox(bytes.NewReader(data), &h.BoxInfo, mp4.BoxPath{mp4.BoxTypeMvhd()})
	if err != nil {
		return WrapError(err, KindParse, locWithFile(name, int64(h.BoxInfo.Offset)), "malformed mvhd")
	}
	if len(mvhdBoxes) == 0 {
		return NewError(KindParse, locWithFile(name, int64(h.BoxInfo.Offset)), "moov has no mvhd")
	}
	trakPaths := mp4.BoxPath{mp4.BoxTypeTrak()}
	traks, err := mp4.ExtractBox(bytes.NewReader(data), &h.BoxInfo, trakPaths)
	if err != nil {
		return WrapError(err, KindParse, locWithFile(name, int64(h.BoxInfo.Offset)), "malformed trak")
	}
	if len(traks) == 0 {
		return NewError(KindParse, locWithFile(name, int64(h.BoxInfo.Offset)), "moov has no trak")
	}
	trexByID := map[uint32]*trexDefaults{}
	mvexBoxes, err := mp4.ExtractBox(bytes.NewReader(data), &h.BoxInfo, mp4.BoxPath{mp4.BoxTypeMvex()})
	if err != nil {
		return WrapError(err, KindParse, locWithFile(name, int64(h.BoxInfo.Offset)), "malformed mvex")
	}
	for _, mv := range mvexBoxes {
		pays, err := mp4.ExtractBoxWithPayload(bytes.NewReader(data), mv, mp4.BoxPath{mp4.BoxTypeTrex()})
		if err != nil {
			return WrapError(err, KindParse, locWithFile(name, int64(mv.Offset)), "malformed trex")
		}
		for _, p := range pays {
			trex := p.Payload.(*mp4.Trex)
			if _, dup := trexByID[trex.TrackID]; dup {
				return NewError(KindParse, locWithFile(name, int64(p.Info.Offset)), "duplicate trex for track %d", trex.TrackID)
			}
			trexByID[trex.TrackID] = &trexDefaults{duration: trex.DefaultSampleDuration, size: trex.DefaultSampleSize}
		}
	}
	seenID := map[uint32]bool{}
	for _, bi := range traks {
		track, err := parseTrak(name, data, bi)
		if err != nil {
			return err
		}
		if seenID[track.SourceID] {
			return NewError(KindParse, locWithFile(name, int64(bi.Offset)), "duplicate track_ID %d", track.SourceID)
		}
		seenID[track.SourceID] = true
		if _, ok := trexByID[track.SourceID]; !ok {
			return NewError(KindParse, locWithFile(name, int64(bi.Offset)), "track %d has no trex", track.SourceID)
		}
		track.defaults = *trexByID[track.SourceID]
		init.Tracks = append(init.Tracks, *track)
	}
	return nil
}

func parseTrak(name string, data []byte, bi *mp4.BoxInfo) (*Track, error) {
	at := func(off int64, format string, args ...any) *Error {
		return NewError(KindParse, locWithFile(name, off), format, args...)
	}
	tkhd, err := firstPayload(data, bi, mp4.BoxPath{mp4.BoxTypeTkhd()})
	if err != nil {
		return nil, WrapError(err, KindParse, locWithFile(name, int64(bi.Offset)), "malformed tkhd")
	}
	if tkhd == nil {
		return nil, at(int64(bi.Offset), "trak has no tkhd")
	}
	mdhd, err := firstPayload(data, bi, mp4.BoxPath{mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()})
	if err != nil {
		return nil, WrapError(err, KindParse, locWithFile(name, int64(bi.Offset)), "malformed mdhd")
	}
	if mdhd == nil {
		return nil, at(int64(bi.Offset), "trak has no mdhd")
	}
	hdlr, err := firstPayload(data, bi, mp4.BoxPath{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()})
	if err != nil {
		return nil, WrapError(err, KindParse, locWithFile(name, int64(bi.Offset)), "malformed hdlr")
	}
	if hdlr == nil {
		return nil, at(int64(bi.Offset), "trak has no hdlr")
	}
	th := tkhd.(*mp4.Tkhd)
	mh := mdhd.(*mp4.Mdhd)
	hd := hdlr.(*mp4.Hdlr)
	track := &Track{
		SourceID:  th.TrackID,
		Timescale: mh.Timescale,
		Handler:   string(hd.HandlerType[:]),
	}
	if track.SourceID == 0 {
		return nil, at(int64(bi.Offset), "track_ID 0 is invalid")
	}
	if track.Timescale == 0 {
		return nil, at(int64(bi.Offset), "track %d has timescale 0", track.SourceID)
	}
	switch track.Handler {
	case "vide":
		track.Kind = MediaVideo
	case "soun":
		track.Kind = MediaAudio
	default:
		return nil, NewError(KindUnsupported, locWithFile(name, int64(bi.Offset)), "track %d handler %q is not audio or video", track.SourceID, track.Handler)
	}
	stsd, err := firstPayload(data, bi, mp4.BoxPath{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd()})
	if err != nil {
		return nil, WrapError(err, KindParse, locWithFile(name, int64(bi.Offset)), "malformed stsd")
	}
	if stsd == nil {
		return nil, at(int64(bi.Offset), "track %d has no stsd", track.SourceID)
	}
	if err := parseSampleEntry(name, data, bi, track); err != nil {
		return nil, err
	}
	track.Key = fmt.Sprintf("%s-%s", track.Kind, track.Codec)
	return track, nil
}

func parseSampleEntry(name string, data []byte, bi *mp4.BoxInfo, track *Track) error {
	entries, err := mp4.ExtractBox(bytes.NewReader(data), bi, mp4.BoxPath{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeAny()})
	if err != nil {
		return WrapError(err, KindParse, locWithFile(name, int64(bi.Offset)), "malformed sample entry")
	}
	var entry *mp4.BoxInfo
	for _, e := range entries {
		switch e.Type {
		case mp4.BoxTypeAvc1(), mp4.BoxTypeHvc1(), mp4.BoxTypeHev1(), mp4.BoxTypeMp4a():
			if entry != nil {
				return NewError(KindUnsupported, locWithFile(name, int64(e.Offset)), "track %d has multiple plausible sample entries", track.SourceID)
			}
			entry = e
		case mp4.BoxTypeEncv(), mp4.BoxTypeEnca():
			return NewError(KindUnsupported, locWithFile(name, int64(e.Offset)), "track %d is encrypted", track.SourceID)
		}
	}
	if entry == nil {
		types := make([]string, 0, len(entries))
		for _, e := range entries {
			types = append(types, e.Type.String())
		}
		return NewError(KindUnsupported, locWithFile(name, int64(bi.Offset)), "track %d has unsupported sample entries %v", track.SourceID, types)
	}
	entryReader := bytes.NewReader(data)
	if _, err := entryReader.Seek(int64(entry.Offset+entry.HeaderSize), 0); err != nil {
		return WrapError(err, KindParse, locWithFile(name, int64(entry.Offset)), "cannot seek to sample entry")
	}
	payload, _, err := mp4.UnmarshalAny(entryReader, entry.Type, entry.Size-entry.HeaderSize, entry.Context)
	if err != nil {
		return WrapError(err, KindTruncated, locWithFile(name, int64(entry.Offset)), "malformed sample entry")
	}
	track.SampleEntry = entry.Type.String()
	switch e := payload.(type) {
	case *mp4.VisualSampleEntry:
		track.Width, track.Height = e.Width, e.Height
		cfgType := mp4.BoxTypeAvcC()
		switch entry.Type {
		case mp4.BoxTypeHvc1(), mp4.BoxTypeHev1():
			track.Codec = CodecHEVC
			cfgType = mp4.BoxTypeHvcC()
		default:
			track.Codec = CodecAVC
		}
		cfg, err := rawChildPayload(data, entry, cfgType)
		if err != nil {
			return WrapError(err, KindParse, locWithFile(name, int64(entry.Offset)), "malformed codec configuration")
		}
		if len(cfg) == 0 {
			return NewError(KindMapping, locWithFile(name, int64(entry.Offset)), "track %d has no %s codec configuration; mapping cannot be lossless", track.SourceID, cfgType.String())
		}
		track.CodecConfig = cfg
	case *mp4.AudioSampleEntry:
		track.Codec = CodecAAC
		if e.GetSampleRateInt() != 0 {
			track.SampleRate = uint32(e.GetSampleRateInt())
		}
		track.ChannelCount = e.ChannelCount
		esds, err := rawChildPayload(data, entry, mp4.BoxTypeEsds())
		if err != nil {
			return WrapError(err, KindParse, locWithFile(name, int64(entry.Offset)), "malformed esds")
		}
		dsi, err := decryptSpecificInfo(esds)
		if err != nil {
			return NewError(KindMapping, locWithFile(name, int64(entry.Offset)), "track %d esds: %v", track.SourceID, err)
		}
		track.CodecConfig = dsi
	default:
		return NewError(KindUnsupported, locWithFile(name, int64(entry.Offset)), "track %d sample entry %q is not supported", track.SourceID, entry.Type.String())
	}
	if track.Kind == MediaAudio && track.SampleRate == 0 {
		track.SampleRate = track.Timescale
	}
	if track.Kind == MediaAudio && track.SampleRate != track.Timescale {
		return NewError(KindMapping, locWithFile(name, int64(entry.Offset)), "track %d audio rate %d differs from timescale %d; lossless mapping is impossible", track.SourceID, track.SampleRate, track.Timescale)
	}
	return nil
}

// decryptSpecificInfo extracts DecoderSpecificInfo from an esds payload.
func decryptSpecificInfo(esds []byte) ([]byte, error) {
	if len(esds) < 4 {
		return nil, errors.New("esds too short")
	}
	pos := 4 // version/flags
	body := esds[pos:]
	var dsi []byte
	err := walkDescriptors(body, func(tag byte, payload []byte) {
		if tag == mp4.DecSpecificInfoTag {
			dsi = append([]byte(nil), payload...)
		}
	})
	if err != nil {
		return nil, err
	}
	if len(dsi) == 0 {
		return nil, errors.New("missing DecoderSpecificInfo")
	}
	return dsi, nil
}

func walkDescriptors(b []byte, visit func(tag byte, payload []byte)) error {
	for len(b) > 0 {
		if len(b) < 2 {
			return errors.New("truncated descriptor header")
		}
		tag := b[0]
		size := 0
		i := 1
		for {
			if i >= len(b) {
				return errors.New("truncated descriptor length")
			}
			c := b[i]
			i++
			size = size<<7 | int(c&0x7f)
			if c&0x80 == 0 {
				break
			}
			if size > len(b) {
				return errors.New("descriptor length overflows")
			}
		}
		if i+size > len(b) {
			return errors.New("descriptor extends past esds end")
		}
		payload := b[i : i+size]
		switch tag {
		case mp4.ESDescrTag:
			// SLConfigDescriptor + a flags byte precede nested descriptors.
			if size < 3 {
				return errors.New("ES descriptor too short")
			}
			flags := payload[2]
			off := 3
			if flags&0x80 != 0 {
				off += 2
			}
			if flags&0x40 != 0 {
				if off >= len(payload) {
					return errors.New("truncated ES descriptor URL length")
				}
				off += 1 + int(payload[off])
			}
			if flags&0x20 != 0 {
				off += 2
			}
			if off > len(payload) {
				return errors.New("ES descriptor nested offset out of range")
			}
			if err := walkDescriptors(payload[off:], visit); err != nil {
				return err
			}
		case mp4.DecoderConfigDescrTag:
			if size < 13 {
				return errors.New("DecoderConfigDescriptor too short")
			}
			if err := walkDescriptors(payload[13:], visit); err != nil {
				return err
			}
		default:
			visit(tag, payload)
		}
		b = b[i+size:]
	}
	return nil
}

func firstPayload(data []byte, parent *mp4.BoxInfo, path mp4.BoxPath) (mp4.IBox, error) {
	boxes, err := mp4.ExtractBoxWithPayload(bytes.NewReader(data), parent, path)
	if err != nil {
		return nil, err
	}
	if len(boxes) == 0 {
		return nil, nil
	}
	return boxes[0].Payload, nil
}

func rawChildPayload(data []byte, parent *mp4.BoxInfo, typ mp4.BoxType) ([]byte, error) {
	boxes, err := mp4.ExtractBox(bytes.NewReader(data), parent, mp4.BoxPath{typ})
	if err != nil {
		return nil, err
	}
	if len(boxes) == 0 {
		return nil, nil
	}
	return payloadBytes(data, boxes[0])
}

func payloadBytes(data []byte, bi *mp4.BoxInfo) ([]byte, error) {
	if bi.Offset+bi.Size > uint64(len(data)) {
		return nil, fmt.Errorf("box at %d extends past segment end", bi.Offset)
	}
	out := make([]byte, bi.Size-bi.HeaderSize)
	copy(out, data[bi.Offset+bi.HeaderSize:bi.Offset+bi.Size])
	return out, nil
}

// ParseSegment demuxes one media segment. init supplies track metadata and
// bounds; trackKey is the stable target key for the samples. file is the
// manifest-relative path; localStart/localEnd select the byte range within
// data (for HLS byte ranges).
func ParseSegment(init *InitSegment, file string, data []byte, trackKey string) (*Segment, error) {
	if len(data) > maxSegmentBytes {
		return nil, NewError(KindParse, locWithFile(file, 0), "segment is %d bytes, limit %d", len(data), maxSegmentBytes)
	}
	byID := map[uint32]Track{}
	for _, tr := range init.Tracks {
		byID[tr.SourceID] = tr
	}
	seg := &Segment{File: file}
	boxes, err := Boxes(data)
	if err != nil {
		return nil, NewError(KindTruncated, locWithFile(file, int64(len(data))), "%v", err)
	}
	seenMoof := false
	for i := 0; i < len(boxes); i++ {
		b := boxes[i]
		switch b.Type {
		case "moof":
			if seenMoof {
				return nil, NewError(KindParse, locWithFile(file, b.Offset), "multiple moof boxes per segment are not supported")
			}
			seenMoof = true
			if i+1 >= len(boxes) || boxes[i+1].Type != "mdat" {
				return nil, NewError(KindBadReference, locWithFile(file, b.Offset), "moof is not followed by mdat")
			}
			if err := parseFragment(init, byID, file, data, b, boxes[i+1], seg, trackKey); err != nil {
				return nil, err
			}
			i++ // consumed mdat
		case "ftyp", "styp", "sidx", "emsg", "prft", "free", "skip":
			// Legal segment-level boxes with no effect on demux.
		case "mdat":
			return nil, NewError(KindBadReference, locWithFile(file, b.Offset), "mdat without a preceding moof")
		default:
			return nil, NewError(KindUnsupported, locWithFile(file, b.Offset), "unexpected top-level box %q", b.Type)
		}
	}
	if !seenMoof {
		return nil, NewError(KindParse, locWithFile(file, 0), "segment contains no moof")
	}
	if seg.TrackID == 0 {
		return nil, NewError(KindParse, locWithFile(file, 0), "segment declares no track")
	}
	if len(seg.Samples) == 0 {
		return nil, NewError(KindParse, locWithFile(file, 0), "segment contains no samples")
	}
	return seg, nil
}

func parseFragment(init *InitSegment, byID map[uint32]Track, file string, data []byte, moof, mdat Box, seg *Segment, trackKey string) error {
	trafs, err := Boxes(moof.Payload)
	if err != nil {
		return NewError(KindTruncated, locWithFile(file, moof.Offset+moof.Header), "malformed moof: %v", err)
	}
	for _, t := range trafs {
		if t.Type != "traf" {
			if t.Type != "mfhd" {
				return NewError(KindUnsupported, locWithFile(file, moof.Offset+moof.Header+t.Offset), "unexpected %q in moof", t.Type)
			}
			continue
		}
		trafOff := moof.Offset + moof.Header + t.Offset
		children, err := Boxes(t.Payload)
		if err != nil {
			return NewError(KindTruncated, locWithFile(file, trafOff+t.Header), "malformed traf: %v", err)
		}
		var tfhd *Box
		var tfdt *Box
		var trun *Box
		for i := range children {
			switch children[i].Type {
			case "tfhd":
				if tfhd != nil {
					return NewError(KindParse, locWithFile(file, trafOff+children[i].Offset), "duplicate tfhd")
				}
				tfhd = &children[i]
			case "tfdt":
				if tfdt != nil {
					return NewError(KindParse, locWithFile(file, trafOff+children[i].Offset), "duplicate tfdt")
				}
				tfdt = &children[i]
			case "trun":
				if trun != nil {
					return NewError(KindUnsupported, locWithFile(file, trafOff+children[i].Offset), "multiple trun per traf are not supported")
				}
				trun = &children[i]
			default:
				return NewError(KindUnsupported, locWithFile(file, trafOff+children[i].Offset), "unexpected %q in traf", children[i].Type)
			}
		}
		if tfhd == nil || trun == nil {
			return NewError(KindBadReference, locWithFile(file, trafOff), "traf requires tfhd and trun")
		}
		if err := fillSamples(init, byID, file, data, moof, mdat, tfhd, tfdt, trun, trafOff, seg, trackKey); err != nil {
			return err
		}
	}
	return nil
}

func fillSamples(init *InitSegment, byID map[uint32]Track, file string, data []byte, moof Box, mdat Box, tfhd, tfdt, trun *Box, trafOff int64, seg *Segment, trackKey string) error {
	if len(tfhd.Payload) < 8 {
		return NewError(KindTruncated, locWithFile(file, trafOff+tfhd.Offset), "tfhd too short")
	}
	flags := uint32(tfhd.Payload[1])<<16 | uint32(tfhd.Payload[2])<<8 | uint32(tfhd.Payload[3])
	trackID := be32(tfhd.Payload[4:])
	track, ok := byID[trackID]
	if !ok {
		return NewError(KindMapping, locWithFile(file, trafOff+tfhd.Offset), "fragment track_ID %d is not in the init segment", trackID)
	}
	if seg.TrackID != 0 && seg.TrackID != trackID {
		return NewError(KindMapping, locWithFile(file, trafOff+tfhd.Offset), "segment mixes track_IDs %d and %d", seg.TrackID, trackID)
	}
	seg.TrackID = trackID
	seg.Timescale = track.Timescale
	var baseDecode uint64
	if tfdt != nil {
		if len(tfdt.Payload) < 8 {
			return NewError(KindTruncated, locWithFile(file, trafOff+tfdt.Offset), "tfdt too short")
		}
		if tfdt.Payload[0] == 1 {
			if len(tfdt.Payload) < 12 {
				return NewError(KindTruncated, locWithFile(file, trafOff+tfdt.Offset), "tfdt v1 too short")
			}
			baseDecode = be64(tfdt.Payload[4:])
		} else {
			baseDecode = uint64(be32(tfdt.Payload[4:]))
		}
	}
	seg.BaseDecodeTime = baseDecode

	if len(trun.Payload) < 8 {
		return NewError(KindTruncated, locWithFile(file, trafOff+trun.Offset), "trun too short")
	}
	trunVersion := trun.Payload[0]
	trunFlags := uint32(trun.Payload[1])<<16 | uint32(trun.Payload[2])<<8 | uint32(trun.Payload[3])
	sampleCount := be32(trun.Payload[4:])
	if int(sampleCount) > maxSamplesPerTrack {
		return NewError(KindParse, locWithFile(file, trafOff+trun.Offset), "trun sample_count %d exceeds limit %d", sampleCount, maxSamplesPerTrack)
	}
	pos := 8
	var dataOffset int64
	if trunFlags&0x000001 != 0 {
		if len(trun.Payload) < pos+4 {
			return NewError(KindTruncated, locWithFile(file, trafOff+trun.Offset), "trun data_offset truncated")
		}
		dataOffset = int64(int32(be32(trun.Payload[pos:])))
		pos += 4
	}
	if trunFlags&0x000004 != 0 {
		pos += 4
	}
	perSample := 0
	if trunFlags&0x000100 != 0 {
		perSample += 4
	}
	if trunFlags&0x000200 != 0 {
		perSample += 4
	}
	if trunFlags&0x000400 != 0 {
		perSample += 4
	}
	if trunFlags&0x000800 != 0 {
		perSample += 4
	}
	if perSample == 0 && sampleCount > 0 {
		return NewError(KindUnsupported, locWithFile(file, trafOff+trun.Offset), "trun has no per-sample sizes; lossless mapping requires sample_size")
	}
	if need := pos + perSample*int(sampleCount); need > len(trun.Payload) {
		return NewError(KindTruncated, locWithFile(file, trafOff+trun.Offset), "trun declares %d samples but payload is short", sampleCount)
	}

	defaultDuration := track.defaults.duration
	defaultSize := track.defaults.size
	// Walk tfhd optional fields in declaration order.
	tp := 8
	if flags&0x000001 != 0 { // base_data_offset
		if len(tfhd.Payload) < tp+8 {
			return NewError(KindTruncated, locWithFile(file, trafOff+tfhd.Offset), "tfhd base_data_offset truncated")
		}
		tp += 8
	}
	if flags&0x000002 != 0 { // sample_description_index
		tp += 4
	}
	if flags&0x000008 != 0 { // default_sample_duration
		if len(tfhd.Payload) < tp+4 {
			return NewError(KindTruncated, locWithFile(file, trafOff+tfhd.Offset), "tfhd default_sample_duration truncated")
		}
		defaultDuration = be32(tfhd.Payload[tp:])
		tp += 4
	}
	if flags&0x000010 != 0 { // default_sample_size
		if len(tfhd.Payload) < tp+4 {
			return NewError(KindTruncated, locWithFile(file, trafOff+tfhd.Offset), "tfhd default_sample_size truncated")
		}
		defaultSize = be32(tfhd.Payload[tp:])
		tp += 4
	}
	if flags&0x000020 != 0 { // default_sample_flags
		tp += 4
	}
	if tp > len(tfhd.Payload) {
		return NewError(KindTruncated, locWithFile(file, trafOff+tfhd.Offset), "tfhd optional fields truncated")
	}
	if trunFlags&0x000100 == 0 && defaultDuration == 0 {
		return NewError(KindUnsupported, locWithFile(file, trafOff+trun.Offset), "sample durations are neither per-sample nor defaulted; timing cannot be expressed")
	}
	if trunFlags&0x000200 == 0 && defaultSize == 0 {
		return NewError(KindUnsupported, locWithFile(file, trafOff+trun.Offset), "sample sizes are neither per-sample nor defaulted")
	}

	if trunFlags&0x000001 == 0 {
		return NewError(KindUnsupported, locWithFile(file, trafOff+trun.Offset), "trun without data_offset is not supported")
	}
	base := moof.Offset
	if flags&0x000001 != 0 {
		if len(tfhd.Payload) < 16 {
			return NewError(KindTruncated, locWithFile(file, trafOff+tfhd.Offset), "tfhd base_data_offset truncated")
		}
		base = int64(be64(tfhd.Payload[8:]))
	} else if flags&0x020000 == 0 {
		return NewError(KindUnsupported, locWithFile(file, trafOff+tfhd.Offset), "tfhd requires base-data-offset-present or default-base-is-moof")
	}
	start := base + dataOffset
	if start < 0 || start > int64(len(data)) {
		return NewError(KindBadReference, locWithFile(file, trafOff+trun.Offset), "sample data offset %d is outside the segment (%d bytes)", start, len(data))
	}
	if mdat.Offset > start || start >= mdat.End() {
		return NewError(KindBadReference, locWithFile(file, start), "sample data offset %d is outside mdat [%d,%d)", start, mdat.Offset, mdat.End())
	}

	dts := baseDecode
	cursor := start
	for i := 0; i < int(sampleCount); i++ {
		entry := pos + i*perSample
		dur := defaultDuration
		size := defaultSize
		var cto int32
		sync := true
		var sampleFlags uint32 = flagSync
		if trunFlags&0x000100 != 0 {
			dur = be32(trun.Payload[entry:])
			entry += 4
		}
		if trunFlags&0x000200 != 0 {
			size = be32(trun.Payload[entry:])
			entry += 4
		}
		if trunFlags&0x000400 != 0 {
			sampleFlags = be32(trun.Payload[entry:])
			entry += 4
		}
		if trunFlags&0x000800 != 0 {
			raw := be32(trun.Payload[entry:])
			if trunVersion == 0 {
				cto = int32(raw)
			} else {
				cto = int32(raw)
			}
			entry += 4
		}
		sync = sampleFlags&0x00010000 == 0
		if dur == 0 {
			return NewError(KindBadReference, locWithFile(file, cursor), "sample %d has zero duration", i)
		}
		if size == 0 {
			return NewError(KindBadReference, locWithFile(file, cursor), "sample %d has zero size", i)
		}
		if cursor+int64(size) > mdat.End() {
			return NewError(KindTruncated, locWithFile(file, cursor), "sample %d needs %d bytes at %d but mdat ends at %d", i, size, cursor, mdat.End())
		}
		dtsTicks, err := Rescale(int64(dts), track.Timescale, BaseTimescale)
		if err != nil {
			return NewError(KindParse, locWithFile(file, cursor), "sample %d DTS overflows the 90 kHz base: %v", i, err)
		}
		durTicks, err := Rescale(int64(dur), track.Timescale, BaseTimescale)
		if err != nil {
			return NewError(KindParse, locWithFile(file, cursor), "sample %d duration overflows the 90 kHz base: %v", i, err)
		}
		ctoTicks, err := Rescale(int64(cto), track.Timescale, BaseTimescale)
		if err != nil {
			return NewError(KindParse, locWithFile(file, cursor), "sample %d composition offset overflows the 90 kHz base: %v", i, err)
		}
		seg.Samples = append(seg.Samples, Sample{
			TrackKey:       trackKey,
			SourceID:       trackID,
			Segment:        file,
			File:           file,
			Index:          len(seg.Samples),
			DTS:            Ticks(dtsTicks),
			PTS:            Ticks(dtsTicks + ctoTicks),
			Duration:       Ticks(durTicks),
			Sync:           sync,
			ByteOffset:     cursor,
			ByteLength:     int64(size),
			Data:           data[cursor : cursor+int64(size)],
			SourceDTS:      dts,
			SourceDuration: dur,
			SourceCto:      cto,
		})
		dts += uint64(dur)
		cursor += int64(size)
	}
	if cursor > mdat.End() {
		return NewError(KindTruncated, locWithFile(file, mdat.End()), "samples overrun mdat")
	}
	return nil
}

// ParseRTPSequenceHeader parses a serialized RTP header (as embedded in
// capture sidecars) and returns its sequence number and marker bit.
func ParseRTPSequenceHeader(raw []byte) (uint16, bool, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return 0, false, err
	}
	return pkt.SequenceNumber, pkt.Marker, nil
}
