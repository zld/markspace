package adapter

import (
	"bytes"
	"encoding/binary"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediacore/media"
)

// mp4Parser carries per-traversal state through the go-mp4 read handler.
type mp4Parser struct {
	a          *MP4Adapter
	budget     int
	fileSize   int64
	tracks     []*mp4TrackState
	curTrack   *mp4TrackState
	movieScale uint32
	parseErrs  []*media.Error
}

type mp4TrackState struct {
	trackID   uint32
	timescale uint32
	video     bool
	stts      []mp4.SttsEntry
	stsc      []mp4.StscEntry
	stsz      *mp4.Stsz
	stco      []uint32
	stss      map[uint32]struct{} // 1-based sync sample numbers
	elst      *mp4.Elst
	hasStsd   bool
	byteStart int64
}

func newMP4Parser(a *MP4Adapter, budget int, size int64) *mp4Parser {
	return &mp4Parser{a: a, budget: budget, fileSize: size}
}

// readU32s decodes count-prefixed uint32 arrays from a full box payload.
// ReadData includes the 4-byte full-box header, which is skipped here. An
// explicit allocation budget prevents corrupt entry counts from causing OOM.
func (p *mp4Parser) readU32s(h *mp4.ReadHandle, field string) ([]uint32, error) {
	var buf bytes.Buffer
	if _, err := h.ReadData(&buf); err != nil {
		return nil, p.locErr(h, media.CodeMalformed, "cannot read %s: %v", field, err)
	}
	raw := buf.Bytes()
	if len(raw) < 8 { // 4 full-box header + 4 entry count
		return nil, p.locErr(h, media.CodeMalformed, "%s payload truncated", field)
	}
	raw = raw[4:] // skip version/flags
	count := binary.BigEndian.Uint32(raw[0:4])
	need := int64(count)*4 + 4
	if need > int64(len(raw)) {
		return nil, p.locErr(h, media.CodeMalformed,
			"%s declares %d entries but payload holds %d bytes", field, count, len(raw)-4)
	}
	if int64(count)*4 > int64(p.budget) {
		return nil, p.locErr(h, media.CodeLimitExceeded, "%s allocation over budget", field)
	}
	out := make([]uint32, count)
	for i := range out {
		out[i] = binary.BigEndian.Uint32(raw[4+4*i:])
	}
	return out, nil
}

func (p *mp4Parser) locErr(h *mp4.ReadHandle, code media.Code, format string, args ...interface{}) *media.Error {
	var off int64 = -1
	var track uint32
	if h != nil {
		off = int64(h.BoxInfo.Offset)
		if p.curTrack != nil {
			track = p.curTrack.trackID
		}
	}
	return media.Errorf(code, track, nil, off, format, args...)
}

// handle is the recursive go-mp4 callback. Container boxes descend; leaf
// sample-table boxes are decoded with explicit bounds.
func (p *mp4Parser) handle(h *mp4.ReadHandle) (interface{}, error) {
	switch h.BoxInfo.Type {
	case mp4.BoxTypeMvhd():
		payload, _, err := h.ReadPayload()
		if err != nil {
			return nil, p.locErr(h, media.CodeMalformed, "mvhd: %v", err)
		}
		if mv, ok := payload.(*mp4.Mvhd); ok {
			p.movieScale = mv.Timescale
		}
		return nil, nil
	case mp4.BoxTypeTrak():
		p.curTrack = &mp4TrackState{stss: map[uint32]struct{}{}, byteStart: int64(h.BoxInfo.Offset)}
		p.tracks = append(p.tracks, p.curTrack)
		return h.Expand()
	case mp4.BoxTypeTkhd():
		payload, _, err := h.ReadPayload()
		if err != nil {
			return nil, p.locErr(h, media.CodeMalformed, "tkhd: %v", err)
		}
		if tk, ok := payload.(*mp4.Tkhd); ok && p.curTrack != nil {
			p.curTrack.trackID = tk.TrackID
		}
		return nil, nil
	case mp4.BoxTypeMdhd():
		payload, _, err := h.ReadPayload()
		if err != nil {
			return nil, p.locErr(h, media.CodeMalformed, "mdhd: %v", err)
		}
		if md, ok := payload.(*mp4.Mdhd); ok && p.curTrack != nil {
			p.curTrack.timescale = md.Timescale
		}
		return nil, nil
	case mp4.BoxTypeHdlr():
		payload, _, err := h.ReadPayload()
		if err != nil {
			return nil, p.locErr(h, media.CodeMalformed, "hdlr: %v", err)
		}
		if hd, ok := payload.(*mp4.Hdlr); ok && p.curTrack != nil {
			p.curTrack.video = hd.HandlerType == mp4.StrToBoxType("vide")
		}
		return nil, nil
	case mp4.BoxTypeStsd():
		if p.curTrack != nil {
			p.curTrack.hasStsd = true
		}
		return h.Expand()
	case mp4.BoxTypeStts():
		payload, _, err := h.ReadPayload()
		if err != nil {
			return nil, p.locErr(h, media.CodeMalformed, "stts: %v", err)
		}
		if st, ok := payload.(*mp4.Stts); ok && p.curTrack != nil {
			p.curTrack.stts = st.Entries
		}
		return nil, nil
	case mp4.BoxTypeStsc():
		payload, _, err := h.ReadPayload()
		if err != nil {
			return nil, p.locErr(h, media.CodeMalformed, "stsc: %v", err)
		}
		if st, ok := payload.(*mp4.Stsc); ok && p.curTrack != nil {
			p.curTrack.stsc = st.Entries
		}
		return nil, nil
	case mp4.BoxTypeStsz():
		payload, _, err := h.ReadPayload()
		if err != nil {
			return nil, p.locErr(h, media.CodeMalformed, "stsz: %v", err)
		}
		if st, ok := payload.(*mp4.Stsz); ok && p.curTrack != nil {
			p.curTrack.stsz = st
		}
		return nil, nil
	case mp4.BoxTypeStco():
		off, err := p.readU32s(h, "stco")
		if err != nil {
			return nil, err
		}
		if p.curTrack != nil {
			p.curTrack.stco = off
		}
		return nil, nil
	case mp4.BoxTypeStss():
		keys, err := p.readU32s(h, "stss")
		if err != nil {
			return nil, err
		}
		if p.curTrack != nil {
			for _, k := range keys {
				p.curTrack.stss[k] = struct{}{}
			}
		}
		return nil, nil
	case mp4.BoxTypeElst():
		payload, _, err := h.ReadPayload()
		if err != nil {
			return nil, p.locErr(h, media.CodeBadEditSegment, "elst: %v", err)
		}
		if el, ok := payload.(*mp4.Elst); ok && p.curTrack != nil {
			p.curTrack.elst = el
		}
		return nil, nil
	default:
		// Only descend into known container boxes. Leaf boxes with unknown
		// types (opaque sample entries) are skipped; asking go-mp4 to
		// unmarshal them would fail on its internal definition table.
		if containerBoxes[h.BoxInfo.Type.String()] {
			return h.Expand()
		}
		return nil, nil
	}
}

// containerBoxes lists the structural boxes that hold child boxes.
var containerBoxes = map[string]bool{
	"moov": true, "trak": true, "edts": true, "mdia": true, "minf": true,
	"dinf": true, "stbl": true, "udta": true, "meta": true, "moof": true,
	"traf": true, "mvex": true,
}

// ParseMP4 runs the full parse and builds a validated Manifest.
func (a *MP4Adapter) ParseMP4(r io.ReadSeeker, size int64) (*Manifest, error) {
	if size < 8 || size > int64(media.MaxParseBytes) {
		return nil, media.Errorf(media.CodeLimitExceeded, 0, nil, -1,
			"file size %d outside parse limits", size)
	}
	p := newMP4Parser(a, media.MaxParseBytes, size)
	if _, err := mp4.ReadBoxStructure(r, p.handle); err != nil {
		if me, ok := media.AsError(err); ok {
			return nil, me
		}
		return nil, media.NewError(media.CodeMalformed, 0, nil, -1, err.Error())
	}
	return p.buildManifest()
}
