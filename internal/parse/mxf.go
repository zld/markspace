package parse

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"

	"replayproof/internal/media"
)

// klvKeyLen is the 16-byte SMPTE universal label that prefixes every KLV
// triplet in this profile.
const klvKeyLen = 16

// maxKLVLength caps a single KLV payload at 64 MiB. The fixtures are far
// smaller; the cap keeps a corrupt BER length from triggering a huge
// allocation before the range check rejects it.
const maxKLVLength = 64 << 20

// Partition key byte 13..15 distinguishes header/body/footer partitions in
// full MXF. This profile only needs the 0x02 header partition marker.
var mxfHeaderPartitionPrefix = []byte{
	0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01, 0x01, 0x02,
}

var mxfHeaderMetadataKey = []byte{
	0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01, 0x01, 0xff, 0x00, 0x00,
}

// isMXFEssenceKey matches the fixture essence key family; the final byte is
// the track number.
func isMXFEssenceKey(k []byte) bool {
	if len(k) < 16 {
		return false
	}
	prefix := []byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x02, 0x01, 0x01, 0x0d, 0x01, 0x03, 0x01, 0x02, 0x00, 0x00}
	for i := range prefix {
		if k[i] != prefix[i] {
			return false
		}
	}
	return true
}

// readBER parses a BER length at data[0:]. It returns the length, the number
// of bytes consumed, and ok=false when the encoding is malformed or longer
// than the available buffer.
func readBER(data []byte) (int, int, bool) {
	if len(data) == 0 {
		return 0, 0, false
	}
	b0 := data[0]
	if b0 < 0x80 {
		return int(b0), 1, true
	}
	n := int(b0 & 0x7f)
	if n == 0 || n > 8 || len(data) < 1+n {
		return 0, 0, false
	}
	var v uint64
	for i := 0; i < n; i++ {
		v = v<<8 | uint64(data[1+i])
		if v > uint64(maxKLVLength) {
			return 0, 0, false
		}
	}
	if v > uint64(maxKLVLength) {
		return 0, 0, false
	}
	return int(v), 1 + n, true
}

// klvAt describes one KLV triplet found by the scanner.
type klvAt struct {
	Key     []byte
	Payload []byte
	Offset  int64
	Size    int
}

// scanMXF walks the file as a flat sequence of KLV triplets and reports the
// first triplet whose declared length leaves the file. Every returned
// triplet has an absolute offset usable for error reporting.
func scanMXF(data []byte) ([]klvAt, error) {
	var out []klvAt
	off := 0
	for off < len(data) {
		if len(data)-off < klvKeyLen {
			return out, &media.Error{
				Code: media.CodeMXFMalformed, Op: "mxf.scan", Offset: int64(off),
				Msg: fmt.Sprintf("partial KLV key at end of file (%d bytes left)", len(data)-off),
			}
		}
		key := data[off : off+klvKeyLen]
		n, hdr, ok := readBER(data[off+klvKeyLen:])
		if !ok {
			return out, &media.Error{
				Code: media.CodeMXFMalformed, Op: "mxf.scan", Offset: int64(off),
				Msg: "invalid BER length",
			}
		}
		payloadStart := off + klvKeyLen + hdr
		if n > len(data)-payloadStart {
			return out, &media.Error{
				Code: media.CodeMXFOutOfRange, Op: "mxf.scan", Offset: int64(off),
				Msg: fmt.Sprintf("KLV declares %d payload bytes, only %d available", n, len(data)-payloadStart),
			}
		}
		out = append(out, klvAt{
			Key:     append([]byte(nil), key...),
			Payload: data[payloadStart : payloadStart+n],
			Offset:  int64(off),
			Size:    klvKeyLen + hdr + n,
		})
		off = payloadStart + n
	}
	return out, nil
}

// localTag is one parsed header metadata entry.
type localTag struct {
	Tag  uint16
	Data []byte
}

// parseLocalTagSet decodes the fixture header metadata as a sequence of
// 2-byte tags with sized values. The value size is implied by the tag's
// width, which keeps the fixture compact; unknown tags are skipped using the
// same width table.
func parseLocalTagSet(data []byte) ([]localTag, error) {
	width := map[uint16]int{
		0x3001: 4, 0x3002: 4, 0x3003: 4, 0x3004: 4, 0x3005: 4, 0x3006: 4,
		0x3007: 4, 0x3008: 2, 0x3009: 2, 0x300a: 1,
		0x300b: 2, 0x300c: 2, 0x300d: 2, 0x300e: 2,
	}
	var out []localTag
	off := 0
	for off < len(data) {
		if len(data)-off < 2 {
			return out, &media.Error{Code: media.CodeMXFMalformed, Op: "mxf.tags", Offset: int64(off), Msg: "partial local tag"}
		}
		tag := binary.BigEndian.Uint16(data[off:])
		w, known := width[tag]
		if !known {
			return out, &media.Error{Code: media.CodeMXFMalformed, Op: "mxf.tags", Offset: int64(off), Msg: fmt.Sprintf("unknown local tag 0x%04x", tag)}
		}
		if len(data)-off-2 < w {
			return out, &media.Error{Code: media.CodeMXFMalformed, Op: "mxf.tags", Offset: int64(off), Msg: fmt.Sprintf("tag 0x%04x value truncated", tag)}
		}
		out = append(out, localTag{Tag: tag, Data: data[off+2 : off+2+w]})
		off += 2 + w
	}
	return out, nil
}

func tagU32(tags []localTag, tag uint16) (uint32, bool) {
	for _, t := range tags {
		if t.Tag == tag && len(t.Data) == 4 {
			return binary.BigEndian.Uint32(t.Data), true
		}
	}
	return 0, false
}

func tagU16(tags []localTag, tag uint16) (uint16, bool) {
	for _, t := range tags {
		if t.Tag == tag && len(t.Data) == 2 {
			return binary.BigEndian.Uint16(t.Data), true
		}
	}
	return 0, false
}

func tagU8(tags []localTag, tag uint16) (uint8, bool) {
	for _, t := range tags {
		if t.Tag == tag && len(t.Data) == 1 {
			return t.Data[0], true
		}
	}
	return 0, false
}

// mxfTrackSpec is the parsed per-track header record.
type mxfTrackSpec struct {
	ID       uint32
	Kind     Kind
	Num      uint32
	Den      uint32
	Count    uint32
	Rotation *int
	Color    *Color
	Crop     *Crop
}

// splitTrackBlocks separates concatenated track records by their 0x3001 id
// tag. The fixture writes records back to back, so a new id tag starts a new
// record.
func splitTrackBlocks(data []byte) ([][]localTag, error) {
	all, err := parseLocalTagSet(data)
	if err != nil {
		return nil, err
	}
	var blocks [][]localTag
	var cur []localTag
	for _, t := range all {
		if t.Tag == 0x3001 && len(cur) > 0 {
			blocks = append(blocks, cur)
			cur = nil
		}
		cur = append(cur, t)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	return blocks, nil
}

// ParseMXF parses the generated operational-pattern MXF into tracks. The
// essence payloads are indexed by byte offset; no decoding occurs.
func ParseMXF(path string) (*Asset, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, media.Wrap(err, media.CodeIO, "mxf.open", -1)
	}
	klvs, err := scanMXF(data)
	if err != nil {
		if me, ok := media.AsError(err); ok {
			me.File = path
		}
		return nil, err
	}
	var header []byte
	headerOffset := int64(0)
	havePartition := false
	for _, k := range klvs {
		switch {
		case len(k.Key) == 16 && bytesEqualPrefix(k.Key, mxfHeaderPartitionPrefix):
			havePartition = true
		case len(k.Key) == 16 && bytesEqual(k.Key, mxfHeaderMetadataKey):
			header = k.Payload
			headerOffset = k.Offset
		}
	}
	if !havePartition {
		return nil, &media.Error{Code: media.CodeMXFMalformed, Op: "mxf.partition", File: path, Offset: 0, Msg: "no header partition pack"}
	}
	if header == nil {
		return nil, &media.Error{Code: media.CodeMXFMalformed, Op: "mxf.header", File: path, Offset: 0, Msg: "no header metadata KLV"}
	}
	blocks, err := splitTrackBlocks(header)
	if err != nil {
		if me, ok := media.AsError(err); ok {
			me.File = path
		}
		return nil, err
	}
	asset := &Asset{Source: "mxf", Path: path}
	byID := make(map[uint32]*Track)
	for _, block := range blocks {
		id, ok := tagU32(block, 0x3001)
		if !ok {
			return nil, &media.Error{Code: media.CodeMXFMalformed, Op: "mxf.track", File: path, Offset: 0, Msg: "track record lacks id"}
		}
		kindVal, ok := tagU32(block, 0x3002)
		if !ok {
			return nil, &media.Error{Code: media.CodeMXFMalformed, Op: "mxf.track", File: path, Offset: 0, Msg: "track record lacks kind"}
		}
		num, ok1 := tagU32(block, 0x3003)
		den, ok2 := tagU32(block, 0x3004)
		count, ok3 := tagU32(block, 0x3005)
		if !ok1 || !ok2 || !ok3 || den == 0 {
			return nil, &media.Error{Code: media.CodeMXFMalformed, Op: "mxf.track", File: path, Offset: 0, Msg: "incomplete track timing"}
		}
		tr := &Track{ID: id, Timescale: den, Meta: &Meta{}, MetaOffset: headerOffset}
		switch kindVal {
		case 1:
			tr.Kind = KindVideo
			tr.Codec = "avc1"
			tr.Width, tr.Height = 1920, 1080
		case 2:
			tr.Kind = KindAudio
			tr.Codec = "mp4a"
			tr.ChannelCount = 2
			tr.SampleRate = den
		default:
			continue
		}
		if rot, ok := tagU32(block, 0x3006); ok {
			deg := int(rot % 360)
			tr.Meta.Rotation = &Rotation{Degrees: deg}
		}
		prim := uint16(0)
		xfer := uint16(0)
		mx := uint16(0)
		haveColor := false
		if v, ok := tagU32(block, 0x3007); ok {
			prim, xfer = uint16(v), uint16(v>>16)
			haveColor = true
		}
		if v, ok := tagU16(block, 0x3009); ok {
			mx = v
			haveColor = true
		}
		full := false
		if v, ok := tagU8(block, 0x300a); ok && v != 0 {
			full = true
		}
		if haveColor {
			tr.Meta.Color = &Color{Primaries: prim, Transfer: xfer, Matrix: mx, FullRange: full}
		}
		if l, ok := tagU16(block, 0x300b); ok {
			top, _ := tagU16(block, 0x300c)
			right, _ := tagU16(block, 0x300d)
			bottom, _ := tagU16(block, 0x300e)
			tr.Meta.Crop = &Crop{Left: uint32(l), Top: uint32(top), Right: uint32(right), Bottom: uint32(bottom)}
		}
		tr.mxfEdit = media.FromTicks(int64(num), den)
		tr.mxfCount = count
		byID[id] = tr
		asset.Tracks = append(asset.Tracks, tr)
	}
	if len(asset.Tracks) == 0 {
		return nil, &media.Error{Code: media.CodeNoTrack, Op: "mxf.track", File: path, Offset: -1, Msg: "no media tracks"}
	}
	// Essence items are assigned to tracks in file order.
	next := make(map[uint32]int)
	for _, k := range klvs {
		if !isMXFEssenceKey(k.Key) {
			continue
		}
		trackID := uint32(k.Key[15])
		tr := byID[trackID]
		if tr == nil {
			return nil, &media.Error{Code: media.CodeMXFMalformed, Op: "mxf.essence", File: path, Offset: k.Offset, Msg: fmt.Sprintf("essence for unknown track %d", trackID)}
		}
		// scanMXF stores the offset of the key, so payload begins after the
		// key and BER length; derive it from the total KLV size.
		hdrLen := k.Size - klvKeyLen - len(k.Payload)
		payloadStart := k.Offset + int64(klvKeyLen+hdrLen)
		idx := next[trackID]
		next[trackID] = idx + 1
		s := Sample{
			Number:   idx + 1,
			DTS:      tr.mxfEdit.MulInt(int64(idx)),
			Duration: tr.mxfEdit,
			Offset:   payloadStart,
			Size:     uint32(len(k.Payload)),
			Keyframe: tr.Kind == KindAudio || (idx%8 == 0),
		}
		s.PTS = s.DTS
		tr.Samples = append(tr.Samples, s)
	}
	for _, tr := range asset.Tracks {
		if tr.mxfCount != 0 && uint32(len(tr.Samples)) != tr.mxfCount {
			return nil, &media.Error{
				Code: media.CodeMXFMalformed, Op: "mxf.essence", File: path, Offset: -1,
				TrackID: tr.ID,
				Msg:     fmt.Sprintf("declared %d samples, found %d", tr.mxfCount, len(tr.Samples)),
			}
		}
		// The fixture interleaves tracks; sort samples by decode time so the
		// per-track map is monotonic.
		sort.SliceStable(tr.Samples, func(i, j int) bool { return tr.Samples[i].DTS.Less(tr.Samples[j].DTS) })
		for i := range tr.Samples {
			tr.Samples[i].Number = i + 1
		}
	}
	asset.Tracks = sortTracks(asset.Tracks)
	return asset, nil
}

func sortTracks(tracks []*Track) []*Track {
	sort.SliceStable(tracks, func(i, j int) bool { return tracks[i].ID < tracks[j].ID })
	return tracks
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func bytesEqualPrefix(a, prefix []byte) bool {
	if len(a) < len(prefix) {
		return false
	}
	return bytesEqual(a[:len(prefix)], prefix)
}
