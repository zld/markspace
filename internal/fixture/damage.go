package fixture

import "encoding/binary"

// Case names emitted in fixtures and manifests.
const (
	CaseNormal       = "normal"
	CaseTruncated    = "truncated_box"
	CaseOutOfRange   = "out_of_range_index"
	CaseCorrupt      = "corrupt_sample"
	CaseMissingIndex = "missing_index"
	CaseClockOverflow = "clock_overflow"
	CaseRTPWindow    = "rtp_window_loss"
)

// Damage describes one applied mutation and the byte offset that forensic
// errors must point back to.
type Damage struct {
	Case   string `json:"case"`
	Offset int64  `json:"offset"`
	Detail string `json:"detail"`
}

// TruncateBox cuts the container inside the final mdat payload and rewrites
// the mdat header so the declared box length runs past EOF. The returned
// offset is the first byte that is missing.
func TruncateBox(data []byte, layout *Layout, dropTail int) ([]byte, Damage, error) {
	if dropTail <= 0 || int64(dropTail) >= layout.MdatPayload {
		return nil, Damage{}, errDamage("truncation length outside mdat payload")
	}
	cut := int64(len(data)) - int64(dropTail)
	out := make([]byte, cut)
	copy(out, data)
	// Declare the original mdat size so the parser detects a length/EOF clash.
	declared := uint32(8 + layout.MdatPayload)
	binary.BigEndian.PutUint32(out[layout.MdatOffset:], declared)
	return out, Damage{
		Case:   CaseTruncated,
		Offset: cut,
		Detail: "mdat declared length extends past EOF",
	}, nil
}

// ShiftChunkOffsets rewrites every stco entry beyond the file end, producing
// an out-of-range index. The returned offset points at the first rewritten
// stco entry inside moov.
func ShiftChunkOffsets(data []byte, layout *Layout, trackID int) ([]byte, Damage, error) {
	tl, ok := layout.Tracks[trackID]
	if !ok || len(tl.ChunkOffset) == 0 {
		return nil, Damage{}, errDamage("track has no chunk offsets")
	}
	out := append([]byte(nil), data...)
	// findStcoOffset returns the position of the stco entry_count field;
	// chunk offset entries begin four bytes after it.
	off, err := findStcoOffset(out, trackID)
	if err != nil {
		return nil, Damage{}, err
	}
	entryCount := binary.BigEndian.Uint32(out[off:])
	first := off + 4
	shift := uint32(len(out)) + 4096
	for i := uint32(0); i < entryCount; i++ {
		pos := int64(first) + int64(i)*4
		binary.BigEndian.PutUint32(out[pos:], binary.BigEndian.Uint32(out[pos:])+shift)
	}
	return out, Damage{
		Case:   CaseOutOfRange,
		Offset: int64(first),
		Detail: "stco entries shifted past container end",
	}, nil
}

// CorruptSample overwrites the payload header of one sample so its magic and
// identity fields are destroyed, simulating localized media rot.
func CorruptSample(data []byte, layout *Layout, trackID, sampleIndex int) ([]byte, Damage, error) {
	tl, ok := layout.Tracks[trackID]
	if !ok || sampleIndex < 0 || sampleIndex >= len(tl.SampleOffset) {
		return nil, Damage{}, errDamage("sample index outside track")
	}
	out := append([]byte(nil), data...)
	off := tl.SampleOffset[sampleIndex]
	for i := int64(0); i < 16; i++ {
		out[off+i] = 0xFF
	}
	return out, Damage{
		Case:   CaseCorrupt,
		Offset: off,
		Detail: "sample magic and identity destroyed",
	}, nil
}

// MissingIndex rebuilds a spec whose target track omits stco and stsc,
// forcing the parser to reconstruct intervals from stsz and mdat geometry.
// Callers regenerate the container with the returned spec; the damage offset
// marks where the stbl table would have started.
func MissingIndex(spec MediaSpec, trackID int) (MediaSpec, Damage) {
	out := cloneSpec(spec)
	for i := range out.Tracks {
		if out.Tracks[i].ID == trackID {
			out.Tracks[i].StblOption.omitStco = true
			out.Tracks[i].StblOption.omitStsc = true
		}
	}
	return out, Damage{
		Case:   CaseMissingIndex,
		Offset: -1, // discovered and overwritten by the parser layer
		Detail: "stco and stsc omitted; boundary rebuild required",
	}
}

// ClockOverflow returns a spec whose audio track declares an absurd
// timescale/delta combination so normalized tick math overflows int64.
func ClockOverflow(spec MediaSpec) MediaSpec {
	out := cloneSpec(spec)
	for i := range out.Tracks {
		if out.Tracks[i].Kind == "soun" {
			out.Tracks[i].Timescale = 1
			out.Tracks[i].Delta = 1<<63 - 1
			out.Tracks[i].SampleCount = 3
		}
	}
	return out
}

// findStcoOffset locates the first stco payload entry of the track whose
// tkhd track_id equals trackID. It performs a bounded structural walk using
// box headers only, so it works on the same byte layout the parser sees.
func findStcoOffset(data []byte, trackID int) (int64, error) {
	moov, err := findBox(data, 0, int64(len(data)), "moov")
	if err != nil {
		return 0, err
	}
	trakOff := moov.payload
	trakEnd := moov.end
	for trakOff < trakEnd {
		trak, err := findBox(data, trakOff, trakEnd, "trak")
		if err != nil {
			return 0, err
		}
		id, ok := trackIDOf(data, trak.payload, trak.end)
		if ok && id == trackID {
			stco, err := findNested(data, trak.payload, trak.end,
				[]string{"mdia", "minf", "stbl", "stco"})
			if err != nil {
				return 0, err
			}
			return stco.payload + 4, nil // skip version+flags, point at entry_count
		}
		trakOff = trak.end
	}
	return 0, errDamage("track id not found while locating stco")
}

type boxRegion struct {
	start, header, payload, end int64
}

func findBox(data []byte, from, limit int64, typ string) (boxRegion, error) {
	off := from
	for off+8 <= limit {
		size := int64(binary.BigEndian.Uint32(data[off:]))
		if size < 8 || off+size > limit {
			return boxRegion{}, errDamage("invalid box header while locating " + typ)
		}
		if string(data[off+4:off+8]) == typ {
			return boxRegion{start: off, header: off + 8, payload: off + 8, end: off + size}, nil
		}
		off += size
	}
	return boxRegion{}, errDamage("box not found: " + typ)
}

func findNested(data []byte, from, limit int64, path []string) (boxRegion, error) {
	cur := boxRegion{payload: from, end: limit}
	for _, name := range path {
		r, err := findBox(data, cur.payload, cur.end, name)
		if err != nil {
			return boxRegion{}, err
		}
		cur = r
	}
	return cur, nil
}

func trackIDOf(data []byte, from, limit int64) (int, bool) {
	tkhd, err := findNested(data, from, limit, []string{"tkhd"})
	if err != nil {
		return 0, false
	}
	// tkhd payload: version/flags(4) create(4) modify(4) track_id(4)
	id := binary.BigEndian.Uint32(data[tkhd.payload+12:])
	return int(id), true
}

func cloneSpec(s MediaSpec) MediaSpec {
	out := s
	out.Tracks = append([]TrackSpec(nil), s.Tracks...)
	return out
}

type damageError string

func (e damageError) Error() string { return "fixture: " + string(e) }

func errDamage(msg string) error { return damageError(msg) }
