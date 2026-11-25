// Package timeline extracts timed metadata events from MP4 containers and
// RTP traces, maps each onto the shared nanosecond timeline, and aligns them
// to the planner's precision and segmentation grid.
package timeline

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/abema/go-mp4"

	"rtpregress/internal/media"
	"rtpregress/internal/rtp"
)

// Source names the metadata carrier an event came from.
type Source string

const (
	SourceSubtitle Source = "subtitle"
	SourceChapter  Source = "chapter"
	SourceID3      Source = "id3"
	SourceEmsg     Source = "emsg"
	SourceSEI      Source = "sei"
)

// Event is one timed metadata observation on the shared timeline.
type Event struct {
	Source     Source
	Track      string
	TimeNs     int64
	DurationNs int64
	Text       string
	Scheme     string
	Offset     int64
	HasOffset  bool
	Sample     int
	File       string
}

// TrackInfo summarises one track discovered while parsing.
type TrackInfo struct {
	Name      string         `json:"name"`
	Handler   string         `json:"handler"`
	Timescale uint32         `json:"timescale"`
	Timebase  media.Timebase `json:"-"`
	Events    int            `json:"events"`
}

// MP4Result is the parsed view of one MP4 fixture.
type MP4Result struct {
	Events        []Event
	Tracks        []TrackInfo
	MVHDTimescale uint32
}

// node is one container box with its absolute file geometry and children.
type node struct {
	typ           mp4.BoxType
	offset        int64
	size          int64
	header        int64
	payloadOffset int64
	children      []*node
}

// find returns the first descendant (including n) with the given type.
func (n *node) find(typ mp4.BoxType) *node {
	if n.typ == typ {
		return n
	}
	for _, c := range n.children {
		if got := c.find(typ); got != nil {
			return got
		}
	}
	return nil
}

// findChild returns only a direct child of the given type.
func (n *node) findChild(typ mp4.BoxType) *node {
	for _, c := range n.children {
		if c.typ == typ {
			return c
		}
	}
	return nil
}

func isContainer(t mp4.BoxType) bool {
	switch t {
	case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeEdts(),
		mp4.BoxTypeDinf(), mp4.BoxTypeUdta(), mp4.BoxTypeMvex(),
		mp4.BoxTypeMoof(), mp4.BoxTypeTraf():
		return true
	}
	return false
}

// readTree parses [start,end) into nodes, descending into container boxes.
// Every structural problem names the offending track, byte offset and size,
// because a truncated box is exactly the failure this kernel must not skip.
func readTree(r *bytes.Reader, start, end int64, track string, depth int) ([]*node, error) {
	var out []*node
	for at := start; at < end; {
		if end-at < 8 {
			return nil, media.At(media.Newf(media.KindInput,
				"truncated box header: %d trailing bytes remain", end-at),
				media.Loc{Track: track, Stage: "parse", Offset: at, HasOffset: true})
		}
		if _, err := r.Seek(at, io.SeekStart); err != nil {
			return nil, media.At(media.New(media.KindInput, err.Error()),
				media.Loc{Track: track, Stage: "parse", Offset: at, HasOffset: true})
		}
		hdr := make([]byte, 8)
		if _, err := io.ReadFull(r, hdr); err != nil {
			return nil, media.At(media.Newf(media.KindInput, "read box header: %v", err),
				media.Loc{Track: track, Stage: "parse", Offset: at, HasOffset: true})
		}
		size := int64(binary.BigEndian.Uint32(hdr[0:4]))
		typ := mp4.BoxType{hdr[4], hdr[5], hdr[6], hdr[7]}
		header := int64(8)
		switch size {
		case 0: // extends to end of enclosing range
			size = end - at
		case 1: // 64-bit largesize
			more := make([]byte, 8)
			if _, err := io.ReadFull(r, more); err != nil {
				return nil, media.At(media.Newf(media.KindInput, "read 64-bit box size: %v", err),
					media.Loc{Track: track, Stage: "parse", Offset: at, HasOffset: true})
			}
			size = int64(binary.BigEndian.Uint64(more))
			header = 16
		}
		if size < header {
			return nil, media.At(media.Newf(media.KindInput,
				"invalid box %s length %d (header %d bytes)", typ.String(), size, header),
				media.Loc{Track: track, Stage: "parse", Offset: at, HasOffset: true})
		}
		if at+size > end {
			return nil, media.At(media.Newf(media.KindInput,
				"truncated box %s: needs %d bytes, only %d remain", typ.String(), size, end-at),
				media.Loc{Track: track, Stage: "parse", Offset: at, HasOffset: true})
		}
		n := &node{typ: typ, offset: at, size: size, header: header, payloadOffset: at + header}
		if isContainer(typ) && depth < 10 {
			kids, err := readTree(r, n.payloadOffset, at+size, track, depth+1)
			if err != nil {
				return nil, err
			}
			n.children = kids
		}
		out = append(out, n)
		at += size
	}
	return out, nil
}

// ParseMP4File reads and parses an MP4 fixture from disk.
func ParseMP4File(path string) (*MP4Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, media.At(media.Newf(media.KindInput, "read %s: %v", path, err),
			media.Loc{Track: "meta", Stage: "parse"})
	}
	return ParseMP4(data, path)
}

// ParseMP4 parses the container in data. Events come from top-level emsg
// boxes and from samples of text, chapter and ID3 tracks.
func ParseMP4(data []byte, name string) (*MP4Result, error) {
	r := bytes.NewReader(data)
	roots, err := readTree(r, 0, int64(len(data)), "meta", 0)
	if err != nil {
		return nil, err
	}
	res := &MP4Result{}
	var moov *node
	for _, b := range roots {
		switch b.typ {
		case mp4.BoxTypeMoov():
			moov = b
		case mp4.BoxTypeEmsg():
			ev, err := parseEmsg(r, b, name)
			if err != nil {
				return nil, err
			}
			res.Events = append(res.Events, ev)
		case mp4.BoxTypeMdat(), mp4.BoxTypeFtyp(), mp4.BoxTypeFree(), mp4.BoxTypeSkip():
			// payload boxes: nothing to extract structurally
		}
	}
	if moov == nil {
		return nil, media.At(media.New(media.KindUnsupported, "MP4 file has no moov box"),
			media.Loc{Track: "meta", Stage: "parse"})
	}
	if mvhd := moov.findChild(mp4.BoxTypeMvhd()); mvhd != nil {
		var m mp4.Mvhd
		if err := unmarshalBox(r, mvhd, &m); err != nil {
			return nil, err
		}
		res.MVHDTimescale = m.Timescale
	}
	for _, trak := range moov.children {
		if trak.typ != mp4.BoxTypeTrak() {
			continue
		}
		if err := parseTrak(r, trak, name, int64(len(data)), res); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func unmarshalBox(r *bytes.Reader, n *node, dst mp4.IBox) error {
	if _, err := r.Seek(n.payloadOffset, io.SeekStart); err != nil {
		return media.At(media.New(media.KindInput, err.Error()),
			media.Loc{Track: "meta", Stage: "parse", Offset: n.offset, HasOffset: true})
	}
	if _, err := mp4.Unmarshal(r, uint64(n.size-n.header), dst, mp4.Context{}); err != nil {
		return media.At(media.Newf(media.KindInput, "parse %s: %v", n.typ.String(), err),
			media.Loc{Track: "meta", Stage: "parse", Offset: n.offset, HasOffset: true})
	}
	return nil
}

func parseEmsg(r *bytes.Reader, n *node, name string) (Event, error) {
	var e mp4.Emsg
	if err := unmarshalBox(r, n, &e); err != nil {
		return Event{}, err
	}
	if e.Timescale == 0 {
		return Event{}, media.At(media.New(media.KindInput, "emsg has zero timescale"),
			media.Loc{Track: "meta", Stage: "parse", Offset: n.offset, HasOffset: true})
	}
	var ticks uint64
	switch e.GetVersion() {
	case 0:
		ticks = uint64(e.PresentationTimeDelta)
	case 1:
		ticks = e.PresentationTime
	default:
		return Event{}, media.At(media.Newf(media.KindUnsupported,
			"emsg version %d", e.GetVersion()),
			media.Loc{Track: "meta", Stage: "parse", Offset: n.offset, HasOffset: true})
	}
	tb := media.Timebase{Track: "meta", Timescale: e.Timescale}
	ns, err := tb.ToNs(ticks)
	if err != nil {
		return Event{}, media.At(err, media.Loc{Track: "meta", Stage: "parse",
			Offset: n.offset, HasOffset: true})
	}
	durNs, err := tb.ToNs(uint64(e.EventDuration))
	if err != nil {
		return Event{}, media.At(err, media.Loc{Track: "meta", Stage: "parse",
			Offset: n.offset, HasOffset: true})
	}
	return Event{
		Source: SourceEmsg, Track: "meta", TimeNs: ns, DurationNs: durNs,
		Text: string(e.MessageData), Scheme: e.SchemeIdUri,
		Offset: n.offset, HasOffset: true, File: name,
	}, nil
}

func parseTrak(r *bytes.Reader, trak *node, name string, fileSize int64, res *MP4Result) error {
	mdia := trak.findChild(mp4.BoxTypeMdia())
	if mdia == nil {
		return media.At(media.New(media.KindInput, "trak has no mdia"),
			media.Loc{Track: "meta", Stage: "parse", Offset: trak.offset, HasOffset: true})
	}
	mdhdNode := mdia.findChild(mp4.BoxTypeMdhd())
	if mdhdNode == nil {
		return media.At(media.New(media.KindInput, "trak has no mdhd"),
			media.Loc{Track: "meta", Stage: "parse", Offset: mdia.offset, HasOffset: true})
	}
	var mdhd mp4.Mdhd
	if err := unmarshalBox(r, mdhdNode, &mdhd); err != nil {
		return err
	}
	hdlrNode := mdia.findChild(mp4.BoxTypeHdlr())
	if hdlrNode == nil {
		return media.At(media.New(media.KindInput, "trak has no hdlr"),
			media.Loc{Track: "meta", Stage: "parse", Offset: mdia.offset, HasOffset: true})
	}
	var hdlr mp4.Hdlr
	if err := unmarshalBox(r, hdlrNode, &hdlr); err != nil {
		return err
	}
	trackName := strings.ToLower(hdlr.Name)
	handler := string(hdlr.HandlerType[:])
	if trackName == "" {
		trackName = handler
	}
	if mdhd.Timescale == 0 {
		return media.At(media.New(media.KindInput, "mdhd has zero timescale"),
			media.Loc{Track: trackName, Stage: "parse", Offset: mdhdNode.offset, HasOffset: true})
	}

	// An edit-list media_time trims the head of the track, so it becomes the
	// track origin: sample 0 presents at media time 0 regardless.
	var originTicks int64
	if edts := trak.findChild(mp4.BoxTypeEdts()); edts != nil {
		if elstNode := edts.findChild(mp4.BoxTypeElst()); elstNode != nil {
			var elst mp4.Elst
			if err := unmarshalBox(r, elstNode, &elst); err != nil {
				return err
			}
			for i := 0; i < int(elst.EntryCount); i++ {
				if mt := elst.GetMediaTime(i); mt >= 0 {
					originTicks = mt
					break
				}
			}
		}
	}
	tb := media.Timebase{Track: trackName, Timescale: mdhd.Timescale, OriginTicks: originTicks}
	if err := tb.Validate(); err != nil {
		return media.At(err, media.Loc{Track: trackName, Stage: "parse",
			Offset: mdhdNode.offset, HasOffset: true})
	}

	stbl := mdia.find(mp4.BoxTypeStbl())
	if stbl == nil {
		return media.At(media.New(media.KindInput, "trak has no stbl"),
			media.Loc{Track: trackName, Stage: "parse", Offset: mdia.offset, HasOffset: true})
	}
	samples, err := readTiming(r, stbl, trackName, fileSize)
	if err != nil {
		return err
	}

	info := TrackInfo{Name: trackName, Handler: handler, Timescale: mdhd.Timescale, Timebase: tb}
	for i, s := range samples {
		off := s.offset
		size := s.size
		if off+int64(size) > fileSize {
			return media.At(media.Newf(media.KindInput,
				"sample %d extends past end of file: offset %d size %d", i+1, off, size),
				media.Loc{Track: trackName, Sample: i + 1, Stage: "parse",
					Offset: off, HasOffset: true})
		}
		ev, err := decodeSample(trackName, r, s, i+1, name, tb)
		if err != nil {
			return err
		}
		if ev != nil {
			res.Events = append(res.Events, *ev)
			info.Events++
		}
	}
	res.Tracks = append(res.Tracks, info)
	return nil
}

// sample is one resolved sample: position, timing and span in ticks.
type sample struct {
	offset int64
	size   int
	start  int64 // in track ticks
	delta  int64 // in track ticks
}

// readTiming resolves stts/stsz/stsc/stco into absolute sample positions.
// The mapping follows ISO/IEC 14496-12: stsc gives samples-per-chunk for each
// chunk run, stco gives each chunk's file offset, and samples are laid out
// consecutively inside a chunk.
func readTiming(r *bytes.Reader, stbl *node, track string, fileSize int64) ([]sample, error) {
	sttsNode := stbl.findChild(mp4.BoxTypeStts())
	stszNode := stbl.findChild(mp4.BoxTypeStsz())
	stscNode := stbl.findChild(mp4.BoxTypeStsc())
	stcoNode := stbl.findChild(mp4.BoxTypeStco())
	if stcoNode == nil {
		// co64 is the 64-bit variant; refuse rather than guess.
		if stbl.findChild(mp4.BoxTypeCo64()) != nil {
			return nil, media.At(media.New(media.KindUnsupported,
				"co64 sample offsets are not supported"),
				media.Loc{Track: track, Stage: "parse", Offset: stbl.offset, HasOffset: true})
		}
	}
	if sttsNode == nil || stszNode == nil || stscNode == nil || stcoNode == nil {
		return nil, media.At(media.New(media.KindInput,
			"sample tables incomplete: need stts, stsz, stsc and stco"),
			media.Loc{Track: track, Stage: "parse", Offset: stbl.offset, HasOffset: true})
	}
	var stts mp4.Stts
	var stsz mp4.Stsz
	var stsc mp4.Stsc
	var stco mp4.Stco
	for _, pair := range []struct {
		n   *node
		dst mp4.IBox
	}{
		{sttsNode, &stts}, {stszNode, &stsz}, {stscNode, &stsc}, {stcoNode, &stco},
	} {
		if err := unmarshalBox(r, pair.n, pair.dst); err != nil {
			return nil, media.At(err, media.Loc{Track: track, Stage: "parse"})
		}
	}

	// Sample durations from the time-to-sample table.
	var starts []sample
	next := int64(0)
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			starts = append(starts, sample{start: next, delta: int64(e.SampleDelta)})
			next += int64(e.SampleDelta)
		}
	}
	if len(starts) == 0 {
		return nil, media.At(media.New(media.KindInput, "stts defines no samples"),
			media.Loc{Track: track, Stage: "parse", Offset: sttsNode.offset, HasOffset: true})
	}
	if uint32(len(starts)) != stsz.SampleCount {
		return nil, media.At(media.Newf(media.KindInput,
			"stts defines %d samples but stsz declares %d", len(starts), stsz.SampleCount),
			media.Loc{Track: track, Stage: "parse", Offset: stszNode.offset, HasOffset: true})
	}

	// Sample sizes.
	if stsz.SampleSize != 0 {
		for i := range starts {
			starts[i].size = int(stsz.SampleSize)
		}
	} else {
		if uint32(len(stsz.EntrySize)) != stsz.SampleCount {
			return nil, media.At(media.Newf(media.KindInput,
				"stsz entry count %d does not match sample count %d", len(stsz.EntrySize), stsz.SampleCount),
				media.Loc{Track: track, Stage: "parse", Offset: stszNode.offset, HasOffset: true})
		}
		for i := range starts {
			starts[i].size = int(stsz.EntrySize[i])
		}
	}

	// Chunk mapping: last stsc entry whose FirstChunk is <= the 1-based chunk
	// index wins.
	if len(stsc.Entries) == 0 {
		return nil, media.At(media.New(media.KindInput, "stsc has no entries"),
			media.Loc{Track: track, Stage: "parse", Offset: stscNode.offset, HasOffset: true})
	}
	sampleIdx := 0
	for chunk, chunkOff := range stco.ChunkOffset {
		chunkNo := uint32(chunk + 1)
		spc := uint32(0)
		for _, e := range stsc.Entries {
			if e.FirstChunk <= chunkNo {
				spc = e.SamplesPerChunk
			}
		}
		if spc == 0 {
			return nil, media.At(media.Newf(media.KindInput,
				"chunk %d has no samples-per-chunk entry", chunkNo),
				media.Loc{Track: track, Stage: "parse", Offset: stscNode.offset, HasOffset: true})
		}
		off := int64(chunkOff)
		for i := uint32(0); i < spc && sampleIdx < len(starts); i++ {
			starts[sampleIdx].offset = off
			off += int64(starts[sampleIdx].size)
			sampleIdx++
		}
		if off > fileSize {
			return nil, media.At(media.Newf(media.KindInput,
				"chunk %d extends past end of file", chunkNo),
				media.Loc{Track: track, Stage: "parse", Offset: int64(chunkOff), HasOffset: true})
		}
	}
	if sampleIdx != len(starts) {
		return nil, media.At(media.Newf(media.KindInput,
			"chunk tables cover %d of %d samples", sampleIdx, len(starts)),
			media.Loc{Track: track, Stage: "parse", Offset: stcoNode.offset, HasOffset: true})
	}
	return starts, nil
}

// decodeSample turns one metadata-track sample into an event. Non-metadata
// tracks return (nil, nil). Times are derived from the track's own timebase,
// so an edited track's first presented sample still lands on media time 0.
func decodeSample(track string, r *bytes.Reader, s sample, index int, file string, tb media.Timebase) (*Event, error) {
	data := make([]byte, s.size)
	if _, err := r.Seek(s.offset, io.SeekStart); err != nil {
		return nil, media.At(media.Newf(media.KindInput, "seek sample %d: %v", index, err),
			media.Loc{Track: track, Sample: index, Stage: "parse", Offset: s.offset, HasOffset: true})
	}
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, media.At(media.Newf(media.KindInput, "read sample %d: %v", index, err),
			media.Loc{Track: track, Sample: index, Stage: "parse", Offset: s.offset, HasOffset: true})
	}
	startNs, err := tb.ToNs(uint64(s.start))
	if err != nil {
		return nil, media.At(err, media.Loc{Track: track, Sample: index, Stage: "parse",
			Offset: s.offset, HasOffset: true})
	}
	delta := s.delta
	if delta < 0 {
		delta = 0
	}
	durNs, err := tb.ToNs(uint64(s.start + delta))
	if err != nil {
		return nil, media.At(err, media.Loc{Track: track, Sample: index, Stage: "parse",
			Offset: s.offset, HasOffset: true})
	}
	durNs -= startNs

	switch track {
	case "subtitle", "chapter":
		if len(data) < 8 {
			return nil, media.At(media.Newf(media.KindInput,
				"%s sample %d is too short for a cue: %d bytes", track, index, len(data)),
				media.Loc{Track: track, Sample: index, Stage: "parse", Offset: s.offset, HasOffset: true})
		}
		textLen := int(binary.BigEndian.Uint16(data[0:2]))
		if 2+textLen > len(data) {
			return nil, media.At(media.Newf(media.KindInput,
				"%s sample %d declares %d text bytes, only %d remain",
				track, index, textLen, len(data)-2),
				media.Loc{Track: track, Sample: index, Stage: "parse", Offset: s.offset, HasOffset: true})
		}
		src := SourceSubtitle
		if track == "chapter" {
			src = SourceChapter
		}
		return &Event{
			Source: src, Track: track, Offset: s.offset, HasOffset: true,
			Sample: index, File: file,
			Text: string(data[2 : 2+textLen]), TimeNs: startNs, DurationNs: durNs,
		}, nil
	case "id3":
		ev, err := parseID3(data, s.offset, index, startNs, durNs, file)
		if err != nil {
			return nil, media.At(err, media.Loc{Track: track, Sample: index, Stage: "parse",
				Offset: s.offset, HasOffset: true})
		}
		return ev, nil
	}
	return nil, nil
}

// parseID3 reads an ID3v2.3/2.4 tag and extracts the RTPREGRESS TXXX frame.
func parseID3(data []byte, offset int64, index int, timeNs, durNs int64, file string) (*Event, error) {
	if len(data) < 10 || string(data[0:3]) != "ID3" {
		return nil, media.Newf(media.KindInput, "sample is not an ID3 tag (%d bytes)", len(data))
	}
	major := data[3]
	if major != 3 && major != 4 {
		return nil, media.Newf(media.KindUnsupported, "unsupported ID3v2.%d tag", major)
	}
	tagSize := synchsafe(data[6:10])
	if 10+tagSize > len(data) {
		return nil, media.Newf(media.KindInput,
			"ID3 tag declares %d bytes but the sample has %d", tagSize, len(data)-10)
	}
	pos := 10
	for pos+10 <= 10+tagSize {
		frameID := string(data[pos : pos+4])
		var frameSize int
		if major == 4 {
			frameSize = synchsafe(data[pos+4 : pos+8])
		} else {
			frameSize = int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		}
		if frameSize < 0 || pos+10+frameSize > 10+tagSize {
			return nil, media.Newf(media.KindInput,
				"ID3 frame %q declares %d bytes, %d remain", frameID, frameSize, 10+tagSize-pos-10)
		}
		if frameID == "TXXX" {
			desc, value, err := decodeTXXX(data[pos+10 : pos+10+frameSize])
			if err != nil {
				return nil, err
			}
			if desc == "RTPREGRESS" {
				return &Event{
					Source: SourceID3, Track: "id3", TimeNs: timeNs, DurationNs: durNs,
					Text: value, Offset: offset, HasOffset: true,
					Sample: index, File: file,
				}, nil
			}
		}
		pos += 10 + frameSize
	}
	return nil, media.New(media.KindInput, "ID3 tag has no RTPREGRESS frame")
}

func decodeTXXX(body []byte) (desc, value string, err error) {
	if len(body) == 0 {
		return "", "", media.New(media.KindInput, "empty TXXX frame")
	}
	if body[0] != 3 {
		return "", "", media.Newf(media.KindUnsupported, "TXXX encoding %d is not UTF-8", body[0])
	}
	rest := body[1:]
	nul := bytes.IndexByte(rest, 0)
	if nul < 0 {
		return "", "", media.New(media.KindInput, "TXXX frame has no description terminator")
	}
	return string(rest[:nul]), string(bytes.TrimSuffix(rest[nul+1:], []byte{0})), nil
}

func synchsafe(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

// ExtractSEI pulls user_data_unregistered SEI messages out of committed
// video packets. Times come from RTP timestamps, never from the SEI body.
func ExtractSEI(t *rtp.Trace, reordered *rtp.ReorderResult) ([]Event, error) {
	var events []Event
	for _, p := range reordered.Packets {
		nals, err := h264NALs(p.Payload)
		if err != nil {
			return nil, media.At(err, media.Loc{Track: t.Header.Track, Sample: p.Line,
				Stage: "parse", Offset: p.Offset, HasOffset: true})
		}
		for _, nal := range nals {
			if nal[0]&0x1f != 6 {
				continue
			}
			text, scheme, err := parseSEI(nal[1:])
			if err != nil {
				return nil, media.At(err, media.Loc{Track: t.Header.Track, Sample: p.Line,
					Stage: "parse", Offset: p.Offset, HasOffset: true})
			}
			if scheme == "" {
				continue
			}
			ns, err := t.TimestampNs(p.Timestamp)
			if err != nil {
				return nil, media.At(err, media.Loc{Track: t.Header.Track, Sample: p.Line,
					Stage: "parse", Offset: p.Offset, HasOffset: true})
			}
			events = append(events, Event{
				Source: SourceSEI, Track: t.Header.Track, TimeNs: ns,
				Text: text, Scheme: scheme,
				Offset: p.Offset, HasOffset: true, Sample: p.Line, File: "video.trace",
			})
		}
	}
	return events, nil
}

// h264NALs splits an RTP payload into complete NAL units.
func h264NALs(payload []byte) ([][]byte, error) {
	if len(payload) == 0 {
		return nil, media.New(media.KindInput, "empty H264 RTP payload")
	}
	switch payload[0] & 0x1f {
	case 28:
		return nil, media.New(media.KindInput, "FU-A fragmented H264 is not supported in offline traces")
	case 24:
		return nil, media.New(media.KindInput, "STAP-A aggregated H264 is not supported in offline traces")
	}
	return [][]byte{payload}, nil
}

// parseSEI walks SEI messages and returns the first user_data_unregistered
// payload: its first 16 bytes are the scheme UUID, the rest is the text.
func parseSEI(data []byte) (text, scheme string, err error) {
	pos := 0
	for pos < len(data) {
		payloadType := 0
		for pos < len(data) && data[pos] == 0xff {
			payloadType += 255
			pos++
		}
		if pos >= len(data) {
			return "", "", media.New(media.KindInput, "SEI payload type truncated")
		}
		payloadType += int(data[pos])
		pos++
		payloadSize := 0
		for pos < len(data) && data[pos] == 0xff {
			payloadSize += 255
			pos++
		}
		if pos >= len(data) {
			return "", "", media.New(media.KindInput, "SEI payload size truncated")
		}
		payloadSize += int(data[pos])
		pos++
		if pos+payloadSize > len(data) {
			return "", "", media.Newf(media.KindInput,
				"SEI payload type %d declares %d bytes, %d remain",
				payloadType, payloadSize, len(data)-pos)
		}
		if payloadType == 5 {
			body := data[pos : pos+payloadSize]
			if len(body) < 16 {
				return "", "", media.Newf(media.KindInput,
					"SEI unregistered payload too short: %d bytes", len(body))
			}
			return string(body[16:]), fmt.Sprintf("%x", body[:16]), nil
		}
		pos += payloadSize
	}
	return "", "", nil
}

// DurationOf returns the furthest event end in nanoseconds.
func DurationOf(events []Event) int64 {
	var max int64
	for _, e := range events {
		if end := e.TimeNs + e.DurationNs; end > max {
			max = end
		}
	}
	return max
}

// SortEvents orders events deterministically by time, track, source and text.
func SortEvents(events []Event) {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.TimeNs != b.TimeNs {
			return a.TimeNs < b.TimeNs
		}
		if a.Track != b.Track {
			return a.Track < b.Track
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Sample != b.Sample {
			return a.Sample < b.Sample
		}
		return a.Text < b.Text
	})
}

// EventSummary is the deterministic digest of one event list. Two runs of the
// same input must produce identical summaries.
type EventSummary struct {
	Count     int            `json:"count"`
	BySource  map[string]int `json:"bySource"`
	MaxTimeNs int64          `json:"maxTimeNs"`
}

// Summarize counts events by carrier and reports the latest end time.
func Summarize(events []Event) EventSummary {
	s := EventSummary{BySource: map[string]int{}, MaxTimeNs: DurationOf(events)}
	s.Count = len(events)
	for _, e := range events {
		s.BySource[string(e.Source)]++
	}
	return s
}

// SourcesSorted returns the source names present, sorted, for stable output.
func (s EventSummary) SourcesSorted() []string {
	out := make([]string, 0, len(s.BySource))
	for k := range s.BySource {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
