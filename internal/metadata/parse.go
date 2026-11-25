// Package metadata parses MP4 timed metadata into the unified track event
// model. It handles generated subtitle (subt), chapter (chap), ID3 (id3 ) and
// SEI (sei ) boxes, plus ISOBMFF emsg boxes. Structural damage (a truncated or
// oversized box) is reported with its byte offset and the producing track,
// while the remaining tracks continue to parse.
//
// The generated archive fixture is a fragmented MP4: a moov declaring one trak
// per logical track (each mdia with its own mdhd timescale and hdlr handler),
// followed by moof boxes whose traf children reference a track via tfhd and
// carry the timed boxes. emsg is decoded with go-mp4; the generated timed boxes
// use the documented layout below so a damaged payload can be located to the
// byte.
package metadata

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"archive.example/mediakernel/internal/clock"
	"archive.example/mediakernel/internal/events"

	"github.com/abema/go-mp4"
)

const (
	// MaxBoxBytes bounds a single timed box payload: 64 KiB.
	MaxBoxBytes = 64 << 10
	// MaxEvents bounds events produced by one document.
	MaxEvents = 20000
	// MaxDepth bounds the box tree recursion.
	MaxDepth = 8
	// MaxInputBytes bounds a whole input document.
	MaxInputBytes = 64 << 20

	// BoxSubtitle is the generated subtitle box type.
	BoxSubtitle = "subt"
	// BoxChapter is the generated chapter box type.
	BoxChapter = "chap"
	// BoxID3 is the generated ID3 timed metadata box type.
	BoxID3 = "id3 "
	// BoxSEI is the generated H.264 SEI timed metadata box type.
	BoxSEI = "sei "
)

// TimedBoxHeaderLen is the fixed prefix of a generated timed box payload:
// version(1) flags(3) presentation(8) duration(4) sequence(2). A NUL-terminated
// UTF-8 text field and the codec payload follow.
const TimedBoxHeaderLen = 18

// TrackSpec is one declared track and its native timebase.
type TrackSpec struct {
	Track      events.TrackID
	TrackID    uint32
	Handler    string
	Timescale  uint32
	OriginNS   int64
	Offset     int64 // mdhd byte offset, for diagnostics
	HasContent bool
}

// Document is a parsed metadata document.
type Document struct {
	Tracks []TrackSpec
	Events []events.Event
	Errors []*events.Error
	// MovieTimescale is the mvhd timescale, used only as a last-resort
	// fallback for boxes that declare no usable track timescale.
	MovieTimescale uint32
}

// FatalErr returns the first fatal parse error, if any.
func (d *Document) FatalErr() *events.Error {
	for _, e := range d.Errors {
		if e.Fatal() {
			return e
		}
	}
	return nil
}

// Converter returns the clock converter for a declared track.
func (d *Document) Converter(track events.TrackID) (*clock.Converter, *events.Error) {
	for _, spec := range d.Tracks {
		if spec.Track == track {
			return clock.NewConverter(clock.Options{
				Track: track, Timescale: spec.Timescale, OriginNS: spec.OriginNS, Width: clock.Width64,
			})
		}
	}
	return nil, events.NewError(events.CodeMalformed, track, 0, -1, "converter", "track not declared", nil)
}

// TrackByID returns the declared track with the given tkhd ID.
func (d *Document) TrackByID(id uint32) (TrackSpec, bool) {
	for _, spec := range d.Tracks {
		if spec.TrackID == id {
			return spec, true
		}
	}
	return TrackSpec{}, false
}

// ParseFile parses an MP4 file from disk.
func ParseFile(path string) (*Document, error) {
	data, err := readFileLimit(path, MaxInputBytes)
	if err != nil {
		return nil, events.NewError(events.CodeMalformed, "", 0, -1, "open", "cannot read MP4 input", err)
	}
	return ParseBytes(data)
}

// ParseBytes parses an MP4 byte stream. Recoverable structural damage is
// recorded in Document.Errors without dropping events parsed elsewhere; fatal
// problems (an invalid timescale) are also recorded for the caller to stop on.
func ParseBytes(data []byte) (*Document, error) {
	p := &parser{data: data, size: int64(len(data)), doc: &Document{}}
	root, errs := parseContainer(data, 0, int64(len(data)), 0)
	p.doc.Errors = append(p.doc.Errors, errs...)
	if errs.hasFatal() {
		return p.doc, p.doc.FatalErr()
	}
	p.resolve(root)
	p.finalize()
	return p.doc, nil
}

// errList accumulates structural errors during tree construction.
type errList []*events.Error

func (l errList) hasFatal() bool {
	for _, e := range l {
		if e.Fatal() {
			return true
		}
	}
	return false
}

// node is one box in the parsed tree. Payload is the raw payload region (which
// may be clipped when the box was truncated).
type node struct {
	typ       string
	offset    int64
	headerLen int64
	size      int64 // parsed size after clipping
	declared  int64 // size field as written
	truncated bool
	payload   []byte
	children  []*node
}

// parseContainer reads sibling boxes in [off,end). It never aborts on
// recoverable damage: a bad box ends that container but errors are returned
// alongside everything parsed before it.
func parseContainer(data []byte, off, end int64, depth int) ([]*node, errList) {
	var nodes []*node
	var errs errList
	if depth > MaxDepth {
		return nil, errList{events.NewError(events.CodeCapacity, "", 0, off, "box",
			fmt.Sprintf("box nesting exceeds %d", MaxDepth), nil)}
	}
	for off < end {
		if end-off < 8 {
			errs = append(errs, events.NewError(events.CodeTruncated, "", 0, off, "box-header",
				fmt.Sprintf("only %d bytes left for a box header", end-off), nil))
			return nodes, errs
		}
		declared := int64(binary.BigEndian.Uint32(data[off : off+4]))
		typ := string(data[off+4 : off+8])
		headerLen := int64(8)
		switch declared {
		case 1:
			if end-off < 16 {
				errs = append(errs, events.NewError(events.CodeTruncated, "", 0, off, "box-header",
					"64-bit box size field is cut off", nil))
				return nodes, errs
			}
			headerLen = 16
			declared = int64(binary.BigEndian.Uint64(data[off+8 : off+16]))
		case 0:
			declared = end - off
		}
		if declared < headerLen {
			errs = append(errs, events.NewError(events.CodeMalformed, "", 0, off, "box",
				fmt.Sprintf("box %q declares size %d below header size %d", typ, declared, headerLen), nil))
			return nodes, errs
		}
		size := declared
		truncated := false
		if off+size > end {
			size = end - off
			truncated = true
			errs = append(errs, events.NewError(events.CodeTruncated, "", 0, off, "truncated",
				fmt.Sprintf("box %q declares %d bytes but only %d remain", typ, declared, size), nil))
		}
		n := &node{
			typ:       typ,
			offset:    off,
			headerLen: headerLen,
			size:      size,
			declared:  declared,
			truncated: truncated,
			payload:   data[off+headerLen : off+size],
		}
		if isContainer(typ) && !truncated {
			children, childErrs := parseContainer(data, off+headerLen, off+size, depth+1)
			n.children = children
			if len(childErrs) > 0 {
				errs = append(errs, childErrs...)
			}
		} else if isContainer(typ) && truncated {
			// Try to recover children of a truncated container up to the cut.
			children, childErrs := parseContainer(data, off+headerLen, off+size, depth+1)
			n.children = children
			// The container-level truncation already explains any child
			// boundary errors; keep only child errors that are not truncation.
			for _, e := range childErrs {
				if e.Code != events.CodeTruncated {
					errs = append(errs, e)
				}
			}
		}
		nodes = append(nodes, n)
		if truncated || size <= 0 {
			return nodes, errs
		}
		off += size
	}
	return nodes, errs
}

func isContainer(typ string) bool {
	switch typ {
	case "moov", "moof", "mdia", "minf", "stbl", "mvex", "trak", "traf", "mfra", "udta":
		return true
	default:
		return false
	}
}

// parser performs the semantic pass over the box tree.
type parser struct {
	data []byte
	size int64
	doc  *Document

	movieScale uint32
	eventCount int
}

// resolve walks the tree, binds track declarations and emits events.
func (p *parser) resolve(root []*node) {
	// Pass 1: top-level movie header and per-trak declarations.
	var moovs, moofs []*node
	for _, n := range root {
		switch n.typ {
		case "moov":
			moovs = append(moovs, n)
		case "moof":
			moofs = append(moofs, n)
		}
	}
	for _, moov := range moovs {
		p.walkForMovie(moov)
	}
	for _, moov := range moovs {
		for _, child := range moov.children {
			if child.typ == "trak" {
				p.resolveTrak(child)
			}
		}
	}
	// Pass 2: media fragments. emsg may sit at moof level or inside traf.
	for _, moof := range moofs {
		p.resolveMoof(moof)
	}
	// Pass 3: top-level emsg (e.g. appended metadata).
	for _, n := range root {
		if n.typ == "emsg" {
			p.resolveEmsg(n, events.TrackEmsg, 0)
		}
	}
	p.doc.MovieTimescale = p.movieScale
}

func (p *parser) walkForMovie(n *node) {
	for _, c := range n.children {
		if c.typ == "mvhd" {
			if mvhd, ok := p.unmarshal(c).(*mp4.Mvhd); ok && mvhd.Timescale != 0 {
				p.movieScale = mvhd.Timescale
			}
		}
		if isContainer(c.typ) {
			p.walkForMovie(c)
		}
	}
}

// resolveTrak binds tkhd id, hdlr handler and mdhd timescale into one spec.
func (p *parser) resolveTrak(trak *node) {
	var spec TrackSpec
	var mdhdNode, hdlrNode *node
	var walk func(n *node)
	walk = func(n *node) {
		for _, c := range n.children {
			switch c.typ {
			case "tkhd":
				if tkhd, ok := p.unmarshal(c).(*mp4.Tkhd); ok {
					spec.TrackID = tkhd.TrackID
				}
			case "hdlr":
				hdlrNode = c
			case "mdhd":
				mdhdNode = c
			}
			if c.typ == "mdia" || c.typ == "minf" || c.typ == "stbl" || c.typ == "udta" {
				walk(c)
			}
		}
	}
	walk(trak)

	if hdlrNode != nil {
		if hdlr, ok := p.unmarshal(hdlrNode).(*mp4.Hdlr); ok {
			spec.Handler = string(hdlr.HandlerType[:])
		}
	}
	if mdhdNode != nil {
		if mdhd, ok := p.unmarshal(mdhdNode).(*mp4.Mdhd); ok {
			spec.Offset = mdhdNode.offset
			spec.Timescale = mdhd.Timescale
			if mdhd.Timescale == 0 {
				p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeInvalidTimescale, spec.Track, 0,
					mdhdNode.offset, "mdhd", "track timescale is zero", nil))
			}
		}
	}
	spec.Track = TrackForHandler(spec.Handler, spec.TrackID)
	if spec.Timescale == 0 {
		p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeInvalidTimescale, spec.Track, 0,
			trak.offset, "trak", "track has no valid timescale", nil))
	}
	p.doc.Tracks = append(p.doc.Tracks, spec)
}

// TrackForHandler maps an ISOBMFF handler type to a unified track identity.
// When a handler type is reused (the generated SEI track shares "vide" with
// the video track) the track name is qualified by the tkhd track ID so the
// two tracks never collapse into one. Unknown handlers fall back to a stable
// per-track name so nothing is lost.
func TrackForHandler(handler string, trackID uint32) events.TrackID {
	switch handler {
	case "vide":
		return events.TrackVideo
	case "soun":
		return events.TrackAudio
	case "subt":
		return events.TrackSubtitle
	case "text", "sbtl":
		return events.TrackChapter
	case "meta", "id3 ":
		return events.TrackID3
	case "":
		return events.TrackID(fmt.Sprintf("track/%d", trackID))
	default:
		return events.TrackID("track/" + handler)
	}
}

// trackForKind names the unified track produced by a timed box. The generated
// SEI track is declared with the video handler (SEI is a video NAL payload),
// so it must be distinguished from the video track itself; the resolved spec's
// track ID provides the stable qualifier.
func trackForKind(kind events.Kind, spec TrackSpec) events.TrackID {
	if kind == events.KindSEI && spec.Track == events.TrackVideo {
		return events.TrackSEI
	}
	return TrackForKind(kind)
}

// resolveMoof walks a fragment, binding each traf to its declared track via
// tfhd and parsing the timed boxes inside it.
func (p *parser) resolveMoof(moof *node) {
	for _, traf := range moof.children {
		if traf.typ == "emsg" {
			p.resolveEmsg(traf, events.TrackEmsg, 0)
			continue
		}
		if traf.typ != "traf" {
			continue
		}
		var spec TrackSpec
		haveSpec := false
		for _, c := range traf.children {
			if c.typ != "tfhd" {
				continue
			}
			if tfhd, ok := p.unmarshal(c).(*mp4.Tfhd); ok {
				if s, found := p.doc.TrackByID(tfhd.TrackID); found {
					spec, haveSpec = s, true
				} else {
					p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeMalformed, "", 0, c.offset,
						"tfhd", fmt.Sprintf("unknown track id %d", tfhd.TrackID), nil))
				}
			}
		}
		for _, c := range traf.children {
			switch c.typ {
			case "emsg":
				p.resolveEmsg(c, events.TrackEmsg, 0)
			case BoxSubtitle, BoxChapter, BoxID3, BoxSEI:
				if !haveSpec {
					p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeMalformed, "", 0, c.offset,
						"timed-box", "timed box outside a resolvable traf", nil))
					continue
				}
				p.resolveTimedBox(c, spec)
			}
		}
	}
}

func kindForBox(typ string) (events.Kind, bool) {
	switch typ {
	case BoxSubtitle:
		return events.KindSubtitle, true
	case BoxChapter:
		return events.KindChapter, true
	case BoxID3:
		return events.KindID3, true
	case BoxSEI:
		return events.KindSEI, true
	default:
		return "", false
	}
}

// TrackForKind returns the unified track that carries a timed box kind.
func TrackForKind(kind events.Kind) events.TrackID {
	switch kind {
	case events.KindSubtitle:
		return events.TrackSubtitle
	case events.KindChapter:
		return events.TrackChapter
	case events.KindID3:
		return events.TrackID3
	case events.KindSEI:
		return events.TrackSEI
	default:
		return events.TrackID(kind)
	}
}

// timedBox is the documented payload layout of the generated timed boxes.
type timedBox struct {
	version      uint8
	presentation uint64
	duration     uint32
	sequence     uint16
	text         string
	payload      []byte
}

func decodeTimedBox(payload []byte) (*timedBox, int, error) {
	if len(payload) < TimedBoxHeaderLen {
		return nil, len(payload), fmt.Errorf("payload %d bytes below fixed header %d", len(payload), TimedBoxHeaderLen)
	}
	tb := &timedBox{
		version:      payload[0],
		presentation: binary.BigEndian.Uint64(payload[4:12]),
		duration:     binary.BigEndian.Uint32(payload[12:16]),
		sequence:     binary.BigEndian.Uint16(payload[16:18]),
	}
	if tb.version != 1 {
		return nil, TimedBoxHeaderLen, fmt.Errorf("unsupported timed box version %d", tb.version)
	}
	rest := payload[TimedBoxHeaderLen:]
	if idx := bytes.IndexByte(rest, 0); idx >= 0 {
		tb.text = string(rest[:idx])
		tb.payload = append([]byte(nil), rest[idx+1:]...)
	} else {
		tb.text = string(rest)
	}
	return tb, len(payload), nil
}

func (p *parser) resolveTimedBox(n *node, spec TrackSpec) {
	kind, ok := kindForBox(n.typ)
	if !ok {
		return
	}
	eventTrack := trackForKind(kind, spec)
	if n.headerLen > n.size {
		p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeTruncated, eventTrack, 0, n.offset,
			"timed-box", "payload is shorter than its header", nil))
		return
	}
	if int64(len(n.payload)) > MaxBoxBytes {
		p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeCapacity, eventTrack, 0, n.offset,
			"timed-box", fmt.Sprintf("payload %d exceeds limit %d", len(n.payload), MaxBoxBytes), nil))
		return
	}
	tb, _, derr := decodeTimedBox(n.payload)
	if derr != nil {
		code := events.CodeMalformed
		if n.truncated {
			code = events.CodeTruncated
		}
		p.doc.Errors = append(p.doc.Errors, events.NewError(code, eventTrack, 0, n.offset, "timed-box", derr.Error(), nil))
		return
	}
	timescale := spec.Timescale
	if timescale == 0 {
		p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeInvalidTimescale, eventTrack, uint64(tb.sequence),
			n.offset, "timed-box", "enclosing track has no valid timescale", nil))
		return
	}
	at, cerr := clock.MapTicks(eventTrack, int64(tb.presentation), timescale, spec.OriginNS, clock.MaxTimelineNS,
		uint64(tb.sequence), n.offset)
	if cerr != nil {
		p.doc.Errors = append(p.doc.Errors, cerr)
		return
	}
	dur, _ := clock.DurationNS(int64(tb.duration), timescale)

	text := tb.text
	payload := tb.payload
	switch kind {
	case events.KindID3:
		if t, rest, ok := DecodeID3(payload); ok {
			if text == "" {
				text = t
			}
			payload = rest
		}
	case events.KindSEI:
		if t, rest, ok := DecodeSEI(payload); ok {
			if text == "" {
				text = t
			}
			payload = rest
		}
	}
	ev := events.Event{
		Track:      eventTrack,
		Kind:       kind,
		At:         at,
		Duration:   dur,
		Seq:        uint64(tb.sequence),
		Text:       text,
		Payload:    payload,
		Source:     "mp4/" + strings.TrimSpace(n.typ),
		ByteOffset: n.offset,
	}
	p.addEvent(ev)
}

// resolveEmsg decodes an emsg box. emsg carries its own timescale, which is
// independent of the enclosing track.
func (p *parser) resolveEmsg(n *node, track events.TrackID, originNS int64) {
	r := bytes.NewReader(n.payload)
	box, _, err := mp4.UnmarshalAny(r, mp4.BoxTypeEmsg(), uint64(len(n.payload)), mp4.Context{})
	if err != nil {
		p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeMalformed, track, 0, n.offset, "emsg", err.Error(), err))
		return
	}
	emsg, ok := box.(*mp4.Emsg)
	if !ok {
		return
	}
	if emsg.Timescale == 0 {
		p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeInvalidTimescale, track, uint64(emsg.Id), n.offset,
			"emsg", "emsg timescale is zero", nil))
		return
	}
	ticks := emsg.PresentationTime
	if emsg.GetVersion() == 0 {
		ticks = uint64(emsg.PresentationTimeDelta)
	}
	at, cerr := clock.MapTicks(track, int64(ticks), emsg.Timescale, originNS, clock.MaxTimelineNS, uint64(emsg.Id), n.offset)
	if cerr != nil {
		p.doc.Errors = append(p.doc.Errors, cerr)
		return
	}
	dur, _ := clock.DurationNS(int64(emsg.EventDuration), emsg.Timescale)
	ev := events.Event{
		Track:      track,
		Kind:       events.KindEmsg,
		At:         at,
		Duration:   dur,
		Seq:        uint64(emsg.Id),
		Text:       emsg.SchemeIdUri + "|" + emsg.Value,
		Payload:    append([]byte(nil), emsg.MessageData...),
		Source:     "mp4/emsg",
		ByteOffset: n.offset,
	}
	p.addEvent(ev)
}

func (p *parser) addEvent(ev events.Event) {
	if p.eventCount >= MaxEvents {
		p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeCapacity, ev.Track, ev.Seq, ev.ByteOffset,
			"parse", fmt.Sprintf("event limit %d reached; event dropped", MaxEvents), nil))
		return
	}
	p.eventCount++
	p.doc.Events = append(p.doc.Events, ev)
}

func (p *parser) unmarshal(n *node) mp4.IBox {
	typ := mp4.StrToBoxType(n.typ)
	r := bytes.NewReader(n.payload)
	box, _, err := mp4.UnmarshalAny(r, typ, uint64(len(n.payload)), mp4.Context{})
	if err != nil {
		p.doc.Errors = append(p.doc.Errors, events.NewError(events.CodeMalformed, "", 0, n.offset, "unmarshal-"+n.typ, err.Error(), err))
		return nil
	}
	return box
}

// finalize sorts events canonically, marks content-bearing tracks and
// converts detail-less structural errors into track-attributed gaps where the
// enclosing fragment already resolved a track.
func (p *parser) finalize() {
	p.doc.Events = events.SortEvents(p.doc.Events)
	sort.SliceStable(p.doc.Tracks, func(i, j int) bool {
		if p.doc.Tracks[i].TrackID != p.doc.Tracks[j].TrackID {
			return p.doc.Tracks[i].TrackID < p.doc.Tracks[j].TrackID
		}
		return string(p.doc.Tracks[i].Track) < string(p.doc.Tracks[j].Track)
	})
	covered := make(map[events.TrackID]bool, len(p.doc.Events))
	for _, ev := range p.doc.Events {
		covered[ev.Track] = true
	}
	for i := range p.doc.Tracks {
		if covered[p.doc.Tracks[i].Track] {
			p.doc.Tracks[i].HasContent = true
		}
	}
}

func readFileLimit(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("input %d bytes exceeds limit %d", info.Size(), limit)
	}
	return io.ReadAll(f)
}
