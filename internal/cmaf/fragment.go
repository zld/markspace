package cmaf

import (
	"encoding/binary"

	"rtx/internal/model"
)

// Sample is one sample pointer inside a fragment file.
type Sample struct {
	Duration uint32 `json:"duration"`
	Size     uint32 `json:"size"`
	Flags    uint32 `json:"flags"`
	// DataOffset is the absolute byte offset of the sample payload in the file.
	DataOffset int64 `json:"data_offset"`
}

// IsSync reports whether the sample is an I-frame (depends_on == 2).
func (s Sample) IsSync() bool { return (s.Flags>>24)&0x3 == 0x2 }

// TrackFragment is one traf.
type TrackFragment struct {
	TrackID uint32 `json:"track_id"`
	// BaseMediaDecodeTime is the tfdt value as stored in the file.
	BaseMediaDecodeTime uint64 `json:"base_media_decode_time"`
	// TFDTOffset is the absolute file offset of the base media decode time
	// field itself, so repairs can name the exact byte they changed.
	TFDTOffset int64 `json:"tfdt_offset"`
	// TFDTBoxOffset is the absolute offset of the tfdt box header.
	TFDTBoxOffset int64    `json:"tfdt_box_offset"`
	Samples       []Sample `json:"samples"`
}

// Fragment is a parsed moof+mdat pair (or an init segment).
type Fragment struct {
	Path   string `json:"path"`
	Source string `json:"source"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
	// MFHDSequence is nil when the moof has no mfhd box. A missing sequence
	// number is recorded as a gap upstream; no duration is inferred.
	MFHDSequence *uint32         `json:"mfhd_sequence"`
	MFHDOffset   int64           `json:"mfhd_offset"`
	MoofOffset   int64           `json:"moof_offset"`
	MoofSize     int64           `json:"moof_size"`
	MdatOffset   int64           `json:"mdat_offset"`
	MdatSize     int64           `json:"mdat_size"`
	Tracks       []TrackFragment `json:"tracks"`
	IsInit       bool            `json:"is_init"`
}

// ParseFragment parses one moof+mdat file. The file must contain exactly one
// moof followed by one mdat; anything else is rejected as unsupported rather
// than skipped.
func ParseFragment(f *File) (*Fragment, error) {
	frag := &Fragment{
		Path:   f.Path,
		Size:   f.Size,
		Digest: hexDigest(f.Digest),
	}
	var moofs, mdats []*Box
	for _, b := range f.Boxes {
		switch b.Type {
		case "moof":
			moofs = append(moofs, b)
		case "mdat":
			mdats = append(mdats, b)
		case "styp", "free", "sidx", "prft", "emsg":
			// recognised but not interpreted
		case "ftyp", "moov":
			frag.IsInit = true
		default:
			return nil, model.E(CodeCMAFInvalid, "parse fragment",
				"unexpected top-level box %q", b.Type).WithPath(f.Path).WithByte(b.Offset)
		}
	}
	if frag.IsInit {
		if len(moofs) != 0 {
			return nil, model.E(CodeCMAFInvalid, "parse fragment", "init segment also contains a moof").WithPath(f.Path)
		}
		return frag, nil
	}
	if len(moofs) != 1 {
		return nil, model.E(CodeCMAFInvalid, "parse fragment", "expected exactly one moof, found %d", len(moofs)).WithPath(f.Path)
	}
	if len(mdats) != 1 {
		return nil, model.E(CodeCMAFInvalid, "parse fragment", "expected exactly one mdat, found %d", len(mdats)).WithPath(f.Path)
	}
	moof, mdat := moofs[0], mdats[0]
	if mdat.Offset < moof.Offset+moof.Size {
		return nil, model.E(CodeCMAFInvalid, "parse fragment", "mdat precedes the end of moof").WithPath(f.Path).WithByte(mdat.Offset)
	}
	frag.MoofOffset, frag.MoofSize = moof.Offset, moof.Size
	frag.MdatOffset, frag.MdatSize = mdat.Offset, mdat.Size

	if mfhd := moof.Find("mfhd"); mfhd != nil {
		if len(mfhd.Payload) < 8 {
			return nil, model.E(CodeCMAFInvalid, "parse mfhd", "mfhd payload is %d bytes", len(mfhd.Payload)).WithPath(f.Path).WithByte(mfhd.Offset)
		}
		seq := binary.BigEndian.Uint32(mfhd.Payload[4:8])
		frag.MFHDSequence = &seq
		frag.MFHDOffset = mfhd.Offset
	}

	for _, trafBox := range moof.FindAll("traf") {
		tf, err := parseTraf(f.Path, moof, trafBox)
		if err != nil {
			return nil, err
		}
		frag.Tracks = append(frag.Tracks, *tf)
	}
	if len(frag.Tracks) == 0 {
		return nil, model.E(CodeCMAFInvalid, "parse fragment", "moof has no traf").WithPath(f.Path).WithByte(moof.Offset)
	}
	return frag, nil
}

func parseTraf(path string, moof, traf *Box) (*TrackFragment, error) {
	tf := &TrackFragment{}
	tfhd := traf.Find("tfhd")
	if tfhd == nil {
		return nil, model.E(CodeCMAFInvalid, "parse traf", "traf has no tfhd").WithPath(path).WithByte(traf.Offset)
	}
	if len(tfhd.Payload) < 8 {
		return nil, model.E(CodeCMAFInvalid, "parse tfhd", "tfhd payload is %d bytes", len(tfhd.Payload)).WithPath(path).WithByte(tfhd.Offset)
	}
	tf.TrackID = binary.BigEndian.Uint32(tfhd.Payload[4:8])
	flags := uint32(tfhd.Payload[1])<<16 | uint32(tfhd.Payload[2])<<8 | uint32(tfhd.Payload[3])
	baseOffset := int64(moof.Offset) // default-base-is-moof
	if flags&0x000001 != 0 && len(tfhd.Payload) >= 16 {
		baseOffset = int64(binary.BigEndian.Uint64(tfhd.Payload[8:16]))
	} else if flags&0x000001 == 0 && flags&0x020000 == 0 {
		// Neither base-data-offset nor default-base-is-moof: the base is the
		// start of the moof under the CMAF profile.
		baseOffset = int64(moof.Offset)
	}

	tfdt := traf.Find("tfdt")
	if tfdt == nil {
		return nil, model.E(CodeCMAFInvalid, "parse traf", "traf has no tfdt").WithPath(path).WithByte(traf.Offset)
	}
	if len(tfdt.Payload) < 8 {
		return nil, model.E(CodeCMAFInvalid, "parse tfdt", "tfdt payload is %d bytes", len(tfdt.Payload)).WithPath(path).WithByte(tfdt.Offset)
	}
	version := tfdt.Payload[0]
	tf.TFDTBoxOffset = tfdt.Offset
	switch version {
	case 0:
		if len(tfdt.Payload) < 8 {
			return nil, model.E(CodeCMAFInvalid, "parse tfdt", "tfdt v0 payload too short").WithPath(path).WithByte(tfdt.Offset)
		}
		tf.BaseMediaDecodeTime = uint64(binary.BigEndian.Uint32(tfdt.Payload[4:8]))
		tf.TFDTOffset = tfdt.Offset + tfdt.HeaderSize + 4
	case 1:
		if len(tfdt.Payload) < 12 {
			return nil, model.E(CodeCMAFInvalid, "parse tfdt", "tfdt v1 payload too short").WithPath(path).WithByte(tfdt.Offset)
		}
		tf.BaseMediaDecodeTime = binary.BigEndian.Uint64(tfdt.Payload[4:12])
		tf.TFDTOffset = tfdt.Offset + tfdt.HeaderSize + 4
	default:
		return nil, model.E(CodeCMAFInvalid, "parse tfdt", "unsupported tfdt version %d", version).WithPath(path).WithByte(tfdt.Offset)
	}

	for _, trunBox := range traf.FindAll("trun") {
		samples, err := parseTrun(path, baseOffset, trunBox)
		if err != nil {
			return nil, err
		}
		tf.Samples = append(tf.Samples, samples...)
	}
	if len(tf.Samples) == 0 {
		return nil, model.E(CodeCMAFInvalid, "parse traf", "track %d has no samples", tf.TrackID).WithPath(path).WithByte(traf.Offset)
	}
	return tf, nil
}

func parseTrun(path string, baseOffset int64, trun *Box) ([]Sample, error) {
	if len(trun.Payload) < 8 {
		return nil, model.E(CodeCMAFInvalid, "parse trun", "trun payload is %d bytes", len(trun.Payload)).WithPath(path).WithByte(trun.Offset)
	}
	flags := uint32(trun.Payload[1])<<16 | uint32(trun.Payload[2])<<8 | uint32(trun.Payload[3])
	count := binary.BigEndian.Uint32(trun.Payload[4:8])
	off := 8
	var dataOffset int64
	if flags&0x000001 != 0 {
		if len(trun.Payload) < off+4 {
			return nil, model.E(CodeCMAFInvalid, "parse trun", "trun data offset missing").WithPath(path).WithByte(trun.Offset)
		}
		dataOffset = int64(int32(binary.BigEndian.Uint32(trun.Payload[off : off+4])))
		off += 4
	}
	var firstFlags uint32
	if flags&0x000004 != 0 {
		if len(trun.Payload) < off+4 {
			return nil, model.E(CodeCMAFInvalid, "parse trun", "trun first sample flags missing").WithPath(path).WithByte(trun.Offset)
		}
		firstFlags = binary.BigEndian.Uint32(trun.Payload[off : off+4])
		off += 4
	}
	entrySize := 0
	for _, bit := range []uint32{0x0100, 0x0200, 0x0400, 0x0800} {
		if flags&bit != 0 {
			entrySize += 4
		}
	}
	if entrySize == 0 {
		return nil, model.E(CodeCMAFInvalid, "parse trun", "trun has no per-sample fields").WithPath(path).WithByte(trun.Offset)
	}
	if need := off + int(count)*entrySize; need > len(trun.Payload) {
		return nil, model.E(CodeCMAFInvalid, "parse trun",
			"trun declares %d samples needing %d bytes, payload has %d", count, need, len(trun.Payload)).
			WithPath(path).WithByte(trun.Offset)
	}
	cursor := baseOffset + dataOffset
	samples := make([]Sample, 0, count)
	for i := 0; i < int(count); i++ {
		e := trun.Payload[off : off+entrySize]
		pos := 0
		var s Sample
		if flags&0x0100 != 0 {
			s.Duration = binary.BigEndian.Uint32(e[pos : pos+4])
			pos += 4
		}
		if flags&0x0200 != 0 {
			s.Size = binary.BigEndian.Uint32(e[pos : pos+4])
			pos += 4
		}
		if flags&0x0400 != 0 {
			s.Flags = binary.BigEndian.Uint32(e[pos : pos+4])
			pos += 4
		}
		if flags&0x0800 != 0 {
			// composition offset; irrelevant for all-intra fixtures
			pos += 4
		}
		if i == 0 && flags&0x000004 != 0 {
			s.Flags = firstFlags
		}
		s.DataOffset = cursor
		// Validate that the sample lies inside the file, not merely inside the
		// declared boxes: an offset past EOF is the first unreadable byte.
		if cursor < 0 || cursor+int64(s.Size) > int64(trun.Offset)+int64(trun.Size) {
			// The sample may legitimately point past trun (into mdat); only
			// detect negatives here, EOF is checked by the caller.
			if cursor < 0 {
				return nil, model.E(CodeCMAFInvalid, "parse trun", "sample %d data offset %d is negative", i, cursor).WithPath(path).WithByte(trun.Offset)
			}
		}
		cursor += int64(s.Size)
		off += entrySize
		samples = append(samples, s)
	}
	return samples, nil
}

func hexDigest(d [32]byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 64)
	for i, b := range d {
		out[i*2] = hexdigits[b>>4]
		out[i*2+1] = hexdigits[b&0x0f]
	}
	return string(out)
}
