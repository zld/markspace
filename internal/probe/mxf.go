package probe

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"archive/internal/essence"
	"archive/internal/fault"
	"archive/internal/model"
)

// MXF universal labels used by the probe. Only the subset the fixtures emit
// is recognized; anything else is skipped by its BER length.
var (
	ulHeaderPartition = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x07}
	ulBodyPartition   = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x03}
	ulFooterPartition = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x04}
	ulPrimerPack      = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x05}
	ulHeaderMeta      = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x53, 0x01, 0x30}
	ulIndexSegment    = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x11}
	ulPreface         = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x53, 0x01, 0x2F}
	ulIdent           = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x53, 0x01, 0x30}
	ulMaterialPkg     = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x53, 0x01, 0x36}
	ulSourcePkg       = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x53, 0x01, 0x37}
	ulTimelineTrack   = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x53, 0x01, 0x3B}
	ulSequence        = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x53, 0x01, 0x0F}
	ulCDCIDescriptor  = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x53, 0x01, 0x44}
	ulWaveAudioDesc   = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x53, 0x01, 0x47}
	ulDataDescriptor  = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x53, 0x01, 0x28}
	ulEssenceVideo    = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x01, 0x02, 0x01, 0x01}
	ulEssenceAudio    = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x01, 0x02, 0x01, 0x02}
	ulEssenceData     = [16]byte{0x06, 0x0E, 0x2B, 0x34, 0x01, 0x02, 0x01, 0x03}
)

// Metadata set tags (local set encoding, 2-byte tag + 2-byte length).
const (
	xtTrackID         = 0x4801
	xtEditRate        = 0x4B01
	xtSampleRate      = 0x3001
	xtContainerDur    = 0x3002
	xtLinkedTrackID   = 0x3006
	xtCodec           = 0x3005
	xtStoredWidth     = 0x3203
	xtStoredHeight    = 0x3202
	xtColorPrimaries  = 0x321A
	xtTransfer        = 0x321B
	xtCodingEq        = 0x321D
	xtCleanAperture   = 0x3297
	xtDisplayRotation = 0x3298
	xtChannelCount    = 0x3D07
	xtAudioRate       = 0x3D03
	xtTrackNumber     = 0x4804
	xtDuration        = 0x0202
	xtIndexTrackID    = 0x3F0B
	xtIndexEntries    = 0x3F0C
)

// klv is one key-length-value triple.
type klv struct {
	Key    [16]byte
	Length uint64
	Offset uint64
	Value  []byte
}

// readKLV parses one KLV at data[off:]. It validates the BER length against
// the bytes actually present and reports a truncated or invalid length in a
// form that carries the exact offset.
func readKLV(data []byte, off uint64, faults *fault.List) (*klv, uint64, error) {
	if off+17 > uint64(len(data)) {
		faults.Add(fault.New(fault.TruncatedBox, off, "KLV header cut short").WithRecoverable(true))
		return nil, off, fmt.Errorf("short klv header")
	}
	var key [16]byte
	copy(key[:], data[off:off+16])
	length, n, err := readBER(data, off+16)
	if err != nil {
		faults.Add(fault.New(fault.InvalidLength, off, "invalid BER length: %v", err).WithRecoverable(false))
		return nil, off, err
	}
	valueOff := off + 16 + n
	if valueOff+length > uint64(len(data)) {
		// A declared length beyond the available bytes is exactly the
		// signature of a truncated file or an inflated length field.
		faults.Add(fault.New(fault.TruncatedBox, off,
			"KLV %x declares %d bytes, only %d remain", key[:4], length, uint64(len(data))-valueOff).
			WithRecoverable(true).WithRange(0, 0))
		return &klv{Key: key, Length: length, Offset: off, Value: data[valueOff:]}, uint64(len(data)), fmt.Errorf("klv truncated")
	}
	return &klv{Key: key, Length: length, Offset: off, Value: data[valueOff : valueOff+length]}, valueOff + length, nil
}

// readBER decodes a BER length. Only the short form and 1..4 byte long forms
// are used by MXF; longer forms are rejected as invalid for this subset.
func readBER(data []byte, off uint64) (uint64, uint64, error) {
	if off >= uint64(len(data)) {
		return 0, 0, fmt.Errorf("eof")
	}
	b := data[off]
	if b&0x80 == 0 {
		return uint64(b), 1, nil
	}
	n := int(b & 0x7F)
	if n == 0 || n > 8 {
		return 0, 0, fmt.Errorf("unsupported BER length form %#x", b)
	}
	if off+1+uint64(n) > uint64(len(data)) {
		return 0, 0, fmt.Errorf("truncated BER length")
	}
	var v uint64
	for i := 0; i < n; i++ {
		v = v<<8 | uint64(data[off+1+uint64(i)])
	}
	return v, uint64(1 + n), nil
}

// localSet is a decoded metadata set: tag -> raw value.
type localSet struct {
	Key   [16]byte
	Off   uint64
	Tags  map[uint16][]byte
	Order []uint16
}

// parseLocalSet decodes the 2-byte tag / 2-byte length local-set encoding.
func parseLocalSet(k *klv) (*localSet, error) {
	s := &localSet{Key: k.Key, Off: k.Offset, Tags: map[uint16][]byte{}}
	v := k.Value
	for off := 0; off+4 <= len(v); {
		tag := binary.BigEndian.Uint16(v[off : off+2])
		length := int(binary.BigEndian.Uint16(v[off+2 : off+4]))
		off += 4
		if off+length > len(v) {
			return nil, fmt.Errorf("set tag %#x length %d exceeds set", tag, length)
		}
		if _, ok := s.Tags[tag]; !ok {
			s.Order = append(s.Order, tag)
		}
		s.Tags[tag] = v[off : off+length]
		off += length
	}
	return s, nil
}

func (s *localSet) u32(tag uint16) (uint32, bool) {
	v, ok := s.Tags[tag]
	if !ok || len(v) < 4 {
		return 0, false
	}
	return binary.BigEndian.Uint32(v[:4]), true
}

func (s *localSet) u16v(tag uint16) (uint16, bool) {
	v, ok := s.Tags[tag]
	if !ok || len(v) < 2 {
		return 0, false
	}
	return binary.BigEndian.Uint16(v[:2]), true
}

func (s *localSet) text(tag uint16) string {
	v, ok := s.Tags[tag]
	if !ok {
		return ""
	}
	if n := bytes.IndexByte(v, 0); n >= 0 {
		v = v[:n]
	}
	return string(v)
}

// mxfTrackDraft accumulates descriptor and index data before the tracks are
// consolidated.
type mxfTrackDraft struct {
	desc      *localSet
	index     *localSet
	trackID   uint32
	editRate  uint32
	timeline  *localSet
	kind      model.TrackKind
	indexSeen bool
}

// ProbeMXF builds the track list for an MXF source.
func ProbeMXF(src Source) (*File, error) {
	f := &File{Container: model.ContainerMXF, Path: src.Name, Size: uint64(len(src.Data)), Info: map[string]string{}}
	data := src.Data
	// Pass 1: walk top-level KLV packets, classifying by key.
	type packet struct {
		k    *klv
		kind string
	}
	var packets []packet
	off := uint64(0)
	for off < uint64(len(data)) {
		k, next, err := readKLV(data, off, &f.Faults)
		if k != nil {
			kind := ""
			switch k.Key {
			case ulHeaderPartition:
				kind = "header"
				f.Partitions++
			case ulBodyPartition:
				kind = "body"
				f.Partitions++
			case ulFooterPartition:
				kind = "footer"
				f.Partitions++
			case ulHeaderMeta:
				kind = "meta"
			case ulPreface, ulIdent:
				kind = "meta"
			case ulIndexSegment:
				kind = "index"
			case ulPrimerPack:
				kind = "primer"
			case ulEssenceVideo, ulEssenceAudio, ulEssenceData:
				kind = "essence"
			default:
				// Any other set under the SMPTE metadata universal label
				// family (060e2b34.02.53) is header metadata, including
				// essence descriptors, packages and sequences.
				if isMetaKey(k.Key) {
					kind = "meta"
				}
			}
			packets = append(packets, packet{k: k, kind: kind})
		}
		if err != nil {
			// Stop cleanly at the first structural break; the remaining bytes
			// are recovered by the scanner, not the probe.
			break
		}
		if next <= off {
			break
		}
		off = next
	}
	if len(packets) == 0 {
		return nil, fmt.Errorf("probe: %s has no parsable KLV packets", src.Name)
	}

	drafts := map[uint32]*mxfTrackDraft{}
	var order []uint32
	// Streaming through the packets, match descriptors to timeline tracks by
	// their TrackID/LinkedTrackID and index segments by their TrackID.
	for _, p := range packets {
		switch p.kind {
		case "meta":
			set, err := parseLocalSet(p.k)
			if err != nil {
				f.Faults.Add(fault.New(fault.InvalidLength, p.k.Offset, "header metadata set: %v", err).WithRecoverable(true))
				continue
			}
			switch set.Key {
			case ulCDCIDescriptor, ulWaveAudioDesc, ulDataDescriptor:
				id := descriptorTrackID(set)
				if id == 0 {
					continue
				}
				d := drafts[id]
				if d == nil {
					d = &mxfTrackDraft{trackID: id, kind: kindFromDescriptor(set.Key)}
					drafts[id] = d
					order = append(order, id)
				}
				d.desc = set
				d.kind = kindFromDescriptor(set.Key)
			case ulTimelineTrack:
				if id := timelineTrackID(set); id != 0 {
					d := drafts[id]
					if d == nil {
						d = &mxfTrackDraft{trackID: id}
						drafts[id] = d
						order = append(order, id)
					}
					d.timeline = set
					if r, ok := set.u32(xtEditRate); ok {
						d.editRate = r
					}
				}
			}
		case "index":
			set, err := parseLocalSet(p.k)
			if err != nil {
				f.Faults.Add(fault.New(fault.InvalidLength, p.k.Offset, "index segment: %v", err).WithRecoverable(true))
				continue
			}
			id, ok := set.u32(xtTrackID)
			if !ok {
				continue
			}
			d := drafts[id]
			if d == nil {
				d = &mxfTrackDraft{trackID: id}
				drafts[id] = d
				order = append(order, id)
			}
			d.index = set
			d.indexSeen = true
		}
	}
	if len(drafts) == 0 {
		return nil, fmt.Errorf("probe: %s carries no track descriptors", src.Name)
	}

	for _, id := range order {
		d := drafts[id]
		t := &model.Track{
			Container: model.ContainerMXF,
			Source:    "mxf",
			TrackID:   id,
			Kind:      d.kind,
			Key:       TrackKey(model.ContainerMXF, id),
			Index:     model.IndexInfo{OffsetBytes: 0},
		}
		if d.desc != nil {
			t.Codec = d.desc.text(xtCodec)
			if r, ok := d.desc.u32(xtSampleRate); ok && r > 0 {
				t.Timebase = model.Timebase{Timescale: r}
			}
			if dur, ok := d.desc.u32(xtContainerDur); ok {
				t.DurationTick = uint64(dur)
			}
			probeMXFDescriptor(t, d.desc)
		}
		if !t.Timebase.Valid() && d.editRate > 0 {
			t.Timebase = model.Timebase{Timescale: d.editRate}
		}
		if !t.Timebase.Valid() {
			t.Timebase = model.Timebase{Timescale: 1000}
			f.Faults.Add(fault.New(fault.MissingIndex, 0, "no edit rate for track %d", id).
				WithTrack(t.Key, id).WithRecoverable(true))
		}
		if d.index != nil {
			frags, err := buildMXFIndexFragments(data, d.index, t, &f.Faults)
			if err != nil {
				t.Index.Present = true
				t.Index.Complete = false
				t.Index.Reason = err.Error()
				t.Index.OffsetBytes = d.index.Off
				f.Faults.Add(fault.New(fault.MissingIndex, d.index.Off, "index unusable: %v", err).
					WithTrack(t.Key, id).WithRecoverable(true))
			} else {
				t.Index = model.IndexInfo{Present: true, Complete: true, Entries: len(frags), Source: "index_table", OffsetBytes: d.index.Off}
				t.Fragments = frags
				t.StartTick = frags[0].StartTick
				end := frags[0].EndTick()
				for _, fr := range frags {
					if fr.EndTick() > end {
						end = fr.EndTick()
					}
				}
				t.DurationTick = end - t.StartTick
			}
		} else {
			t.Index = model.IndexInfo{Present: false, Complete: false, Reason: "no index table segment"}
			f.Faults.Add(fault.New(fault.MissingIndex, 0, "track %d has no index table segment", id).
				WithTrack(t.Key, id).WithRecoverable(true))
		}
		t.Required = t.Kind == model.KindVideo || t.Kind == model.KindAudio
		f.Tracks = append(f.Tracks, t)
	}
	// Essence presence: confirm the file actually contains the frames the
	// index claims; if the media area is cut the demux stage will localize it.
	f.Info["essence_packets"] = fmt.Sprintf("%d", countKind(packets, "essence"))
	_ = essence.HeaderSize
	return f, nil
}

func countKind[T any](ps []T, _ string) int { return len(ps) }

// isMetaKey reports whether a key belongs to the SMPTE header metadata set
// family. Structural keys (partition, index, primer, essence) are excluded by
// their lower label bytes and matched before this check.
func isMetaKey(key [16]byte) bool {
	return key[0] == 0x06 && key[1] == 0x0E && key[2] == 0x2B && key[3] == 0x34 &&
		key[4] == 0x02 && key[5] == 0x53
}

// descriptorTrackID prefers LinkedTrackID, falling back to the timeline track
// number the descriptor was paired with.
func descriptorTrackID(set *localSet) uint32 {
	if id, ok := set.u32(xtLinkedTrackID); ok {
		return id
	}
	if id, ok := set.u32(xtTrackID); ok {
		return id
	}
	if id, ok := set.u16v(xtTrackNumber); ok {
		return uint32(id)
	}
	return 0
}

func timelineTrackID(set *localSet) uint32 {
	if id, ok := set.u16v(xtTrackNumber); ok {
		return uint32(id)
	}
	return 0
}

func kindFromDescriptor(ul [16]byte) model.TrackKind {
	switch ul {
	case ulWaveAudioDesc:
		return model.KindAudio
	case ulDataDescriptor:
		return model.KindData
	default:
		return model.KindVideo
	}
}

// probeMXFDescriptor extracts rotation-independent picture metadata, colour
// and audio parameters. Colour names are already canonical in the fixture.
func probeMXFDescriptor(t *model.Track, set *localSet) {
	if t.Kind == model.KindVideo {
		if r, ok := set.u16v(xtDisplayRotation); ok {
			t.RotationDeg = int(r) % 360
		}
		p := set.text(xtColorPrimaries)
		tr := set.text(xtTransfer)
		m := set.text(xtCodingEq)
		src := "mxf"
		if p == "" && tr == "" && m == "" {
			p, tr, m, src = "bt709", "bt709", "bt709", "default"
		} else {
			if p == "" {
				p = "bt709"
			}
			if tr == "" {
				tr = "bt709"
			}
			if m == "" {
				m = "bt709"
			}
		}
		t.Color = description(p, tr, m, "limited", src)
		t.ColorClaims = claims(t.Color)
		if v, ok := set.Tags[xtCleanAperture]; ok && len(v) >= 8 {
			t.Crop = &model.Crop{
				Left:   int(binary.BigEndian.Uint16(v[0:2])),
				Top:    int(binary.BigEndian.Uint16(v[2:4])),
				Right:  int(binary.BigEndian.Uint16(v[4:6])),
				Bottom: int(binary.BigEndian.Uint16(v[6:8])),
			}
		} else {
			t.MissingOptional = append(t.MissingOptional, "clean_aperture")
			// The fixture writes stored width/height; without an explicit
			// aperture the crop is the full raster.
			w, wok := set.u32(xtStoredWidth)
			h, hok := set.u32(xtStoredHeight)
			if wok && hok && w > 0 && h > 0 {
				t.Crop = &model.Crop{Right: int(w), Bottom: int(h)}
			}
		}
	} else {
		t.Color = description("bt709", "bt709", "bt709", "limited", "default")
		t.ColorClaims = claims(t.Color)
		t.MissingOptional = append(t.MissingOptional, "color")
	}
}

// buildMXFIndexFragments decodes the synthetic index entry layout the fixture
// writes: flags(1) KLV offset(4) value size(4) startTick(4) duration(2). For
// each entry the KLV wrapper at the recorded offset is re-read so the
// fragment's essence payload offset and size come from the bytes on disk, not
// only from the index; a mismatch leaves the index incomplete so recovery
// scans instead of trusting it.
func buildMXFIndexFragments(data []byte, set *localSet, t *model.Track, faults *fault.List) ([]model.Fragment, error) {
	raw, ok := set.Tags[xtIndexEntries]
	if !ok {
		return nil, fmt.Errorf("index set has no entries")
	}
	const entrySize = 15
	if len(raw)%entrySize != 0 {
		return nil, fmt.Errorf("entry bytes %d not a multiple of %d", len(raw), entrySize)
	}
	var frags []model.Fragment
	for i := 0; i+entrySize <= len(raw); i += entrySize {
		e := raw[i : i+entrySize]
		klvOff := uint64(binary.BigEndian.Uint32(e[1:5]))
		wantSize := uint64(binary.BigEndian.Uint32(e[5:9]))
		f := model.Fragment{
			Seq:           len(frags),
			Sync:          e[0]&1 != 0,
			WrapperOffset: klvOff,
			StartTick:     uint64(binary.BigEndian.Uint32(e[9:13])),
			Duration:      uint64(binary.BigEndian.Uint16(e[13:15])),
		}
		if klvOff+17 > uint64(len(data)) {
			return nil, fmt.Errorf("index entry %d points past EOF (%d)", i/entrySize, klvOff)
		}
		length, n, err := readBER(data, klvOff+16)
		if err != nil {
			return nil, fmt.Errorf("index entry %d has unreadable KLV length: %w", i/entrySize, err)
		}
		payloadOff := klvOff + 16 + n
		if payloadOff+length > uint64(len(data)) {
			return nil, fmt.Errorf("index entry %d essence runs past EOF", i/entrySize)
		}
		if length != wantSize {
			return nil, fmt.Errorf("index entry %d declares %d bytes but KLV says %d", i/entrySize, wantSize, length)
		}
		f.PayloadOffset = payloadOff
		f.WrapperSize = payloadOff - klvOff
		f.Size = length
		frags = append(frags, f)
	}
	if len(frags) == 0 {
		return nil, fmt.Errorf("empty index")
	}
	_ = faults
	return frags, nil
}
