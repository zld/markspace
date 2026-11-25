package media

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"

	"github.com/zld/media-regress/internal/error"
)

// The MP4 parser walks the box tree itself rather than using go-mp4's
// ReadBoxStructure. The regression profile needs raw offsets, per-entry stsd
// expansion and precise truncation reporting, all of which are simpler with an
// explicit cursor; go-mp4 still supplies every payload decoder.

// boxNode is one parsed box in the tree.
type boxNode struct {
	typ    mp4.BoxType
	offset int64 // box header start
	size   int64 // total box size, header included
	header int64
	kids   []*boxNode
}

func (n *boxNode) payloadStart() int64 { return n.offset + n.header }
func (n *boxNode) payloadSize() int64  { return n.size - n.header }
func (n *boxNode) find(typ mp4.BoxType) *boxNode {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.typ == typ {
			return k
		}
	}
	return nil
}
func (n *boxNode) all(typ mp4.BoxType) []*boxNode {
	var out []*boxNode
	if n == nil {
		return out
	}
	for _, k := range n.kids {
		if k.typ == typ {
			out = append(out, k)
		}
	}
	return out
}

// containerTypes are the boxes whose payload is a sequence of child boxes.
var containerTypes = map[mp4.BoxType]bool{
	mp4.StrToBoxType("moov"): true, mp4.StrToBoxType("trak"): true,
	mp4.StrToBoxType("mdia"): true, mp4.StrToBoxType("minf"): true,
	mp4.StrToBoxType("stbl"): true, mp4.StrToBoxType("dinf"): true,
	mp4.StrToBoxType("edts"): true, mp4.StrToBoxType("udta"): true,
}

func probeMP4(data []byte) bool {
	if len(data) < 8 {
		return false
	}
	typ := string(data[4:8])
	return typ == "ftyp" || typ == "moov" || typ == "styp"
}

// walkBoxes parses the box sequence in data. tree reports container nesting.
func walkBoxes(data []byte, base int64, depth int) ([]*boxNode, error) {
	if depth > 8 {
		return nil, merr.New(merr.StageProbe, merr.KindParse, 0, base, "mp4: box nesting too deep")
	}
	var out []*boxNode
	pos := int64(0)
	for pos < int64(len(data)) {
		if int64(len(data))-pos < 8 {
			return nil, merr.New(merr.StageProbe, merr.KindTruncated, 0, base+pos,
				"mp4: %d trailing bytes cannot form a box header", int64(len(data))-pos)
		}
		size := int64(binary.BigEndian.Uint32(data[pos:]))
		typ := mp4.BoxType{data[pos+4], data[pos+5], data[pos+6], data[pos+7]}
		header := int64(8)
		switch size {
		case 0:
			size = int64(len(data)) - pos
		case 1:
			if int64(len(data))-pos < 16 {
				return nil, merr.New(merr.StageProbe, merr.KindTruncated, 0, base+pos,
					"mp4: truncated large box header")
			}
			size = int64(binary.BigEndian.Uint64(data[pos+8:]))
			header = 16
		}
		if size < header {
			return nil, merr.New(merr.StageProbe, merr.KindCorrupt, 0, base+pos,
				"mp4: box %s declares size %d smaller than its header", typ.String(), size)
		}
		if pos+size > int64(len(data)) {
			return nil, merr.New(merr.StageProbe, merr.KindTruncated, 0, base+pos,
				"mp4: box %s declares size %d but only %d bytes remain", typ.String(), size, int64(len(data))-pos)
		}
		node := &boxNode{typ: typ, offset: base + pos, size: size, header: header}
		if containerTypes[typ] {
			kids, err := walkBoxes(data[pos+header:pos+size], base+pos+header, depth+1)
			if err != nil {
				return nil, err
			}
			node.kids = kids
		}
		out = append(out, node)
		pos += size
	}
	return out, nil
}

// decode unmarshals the node payload into a fresh go-mp4 box.
func decodeNode(data []byte, n *boxNode, ctx mp4.Context) (mp4.IBox, error) {
	box, err := n.typ.New(ctx)
	if err != nil {
		return nil, merr.Wrap(merr.StageProbe, merr.KindParse, 0, n.offset, err, "mp4: cannot decode %s", n.typ.String())
	}
	payload := data[n.payloadStart() : n.payloadStart()+n.payloadSize()]
	if _, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), box, ctx); err != nil {
		return nil, merr.Wrap(merr.StageProbe, merr.KindParse, 0, n.payloadStart(), err, "mp4: %s payload", n.typ.String())
	}
	return box, nil
}

// rotationFromMatrix derives the clockwise display rotation in degrees.
func rotationFromMatrix(m [9]int32) (int, error) {
	a, b := m[0], m[1]
	switch {
	case a == 0x00010000 && b == 0:
		return 0, nil
	case a == 0 && b == 0x00010000:
		return 90, nil
	case a == -0x00010000 && b == 0:
		return 180, nil
	case a == 0 && b == -0x00010000:
		return 270, nil
	default:
		return 0, fmt.Errorf("unsupported matrix [%d %d ...]", a, b)
	}
}

// parseMP4 builds an Asset from MP4 bytes.
func parseMP4(data []byte, source, digest string) (*Asset, error) {
	roots, err := walkBoxes(data, 0, 0)
	if err != nil {
		return nil, err
	}
	var moov *boxNode
	for _, r := range roots {
		if r.typ == mp4.BoxTypeMoov() {
			if moov != nil {
				return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, 0, r.offset, "mp4: duplicate moov")
			}
			moov = r
		}
	}
	if moov == nil {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, 0, 0, "mp4: missing moov")
	}
	asset := &Asset{Format: "mp4", Source: source, Digest: digest}
	for _, trak := range moov.all(mp4.BoxTypeTrak()) {
		track, err := parseMP4Track(data, trak)
		if err != nil {
			return nil, err
		}
		track.Source = source
		track.Normalize()
		if err := track.Validate(); err != nil {
			return nil, err
		}
		asset.Tracks = append(asset.Tracks, track)
	}
	if len(asset.Tracks) == 0 {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, 0, 0, "mp4: no tracks")
	}
	for _, t := range asset.Tracks {
		asset.Conflicts += t.Conflicts
	}
	return asset, nil
}

func parseMP4Track(data []byte, trak *boxNode) (*Track, error) {
	tkhdNode := trak.find(mp4.BoxTypeTkhd())
	if tkhdNode == nil {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, 0, trak.offset, "mp4: trak without tkhd")
	}
	tkhdBox, err := decodeNode(data, tkhdNode, mp4.Context{})
	if err != nil {
		return nil, err
	}
	tkhd := tkhdBox.(*mp4.Tkhd)
	mdia := trak.find(mp4.BoxTypeMdia())
	if mdia == nil {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, tkhd.TrackID, trak.offset, "mp4: trak without mdia")
	}
	mdhdNode := mdia.find(mp4.BoxTypeMdhd())
	hdlrNode := mdia.find(mp4.BoxTypeHdlr())
	if mdhdNode == nil || hdlrNode == nil {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, tkhd.TrackID, mdia.offset,
			"mp4: mdia without mdhd or hdlr")
	}
	mdhdBox, err := decodeNode(data, mdhdNode, mp4.Context{})
	if err != nil {
		return nil, err
	}
	hdlrBox, err := decodeNode(data, hdlrNode, mp4.Context{})
	if err != nil {
		return nil, err
	}
	mdhd := mdhdBox.(*mp4.Mdhd)
	hdlr := hdlrBox.(*mp4.Hdlr)

	track := &Track{
		ID:          tkhd.TrackID,
		Timescale:   mdhd.Timescale,
		Duration:    int64(mdhd.GetDuration()),
		InputOffset: trak.offset,
	}
	switch hdlr.HandlerType {
	case [4]byte{'v', 'i', 'd', 'e'}:
		track.Kind = KindVideo
	case [4]byte{'s', 'o', 'u', 'n'}:
		track.Kind = KindAudio
	default:
		return nil, merr.New(merr.StageMetadata, merr.KindParse, tkhd.TrackID, hdlrNode.offset,
			"mp4: unsupported handler type %q", string(hdlr.HandlerType[:]))
	}
	if track.Timescale == 0 {
		return nil, merr.New(merr.StageMetadata, merr.KindInconsistent, track.ID, mdhdNode.offset,
			"mp4: mdhd timescale is zero")
	}

	stbl := mdia.find(mp4.BoxTypeMinf()).find(mp4.BoxTypeStbl())
	if stbl == nil {
		return nil, merr.New(merr.StageMetadata, merr.KindParse, track.ID, mdia.offset, "mp4: missing stbl")
	}
	if err := parseMP4Descriptions(data, track, tkhd, stbl); err != nil {
		return nil, err
	}
	if track.Kind == KindAudio {
		if err := parseMP4AudioSamples(data, track, stbl); err != nil {
			return nil, err
		}
	} else if err := parseMP4VideoTiming(data, track, stbl); err != nil {
		return nil, err
	}
	return track, nil
}
