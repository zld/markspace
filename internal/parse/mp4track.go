package parse

import (
	"encoding/binary"
	"fmt"
	"os"
	"sort"

	"github.com/abema/go-mp4"

	"replayproof/internal/media"
)

func parseTrak(f *os.File, path string, trak *boxNode) (*Track, error) {
	tkhdNode := trak.child("tkhd")
	mdia := trak.child("mdia")
	if mdia == nil {
		return nil, media.New(media.CodeNoTrack, "mp4.mdia", int64(trak.Info.Offset), "trak lacks mdia")
	}
	mdhdNode := mdia.child("mdhd")
	hdlrNode := mdia.child("hdlr")
	if tkhdNode == nil || mdhdNode == nil || hdlrNode == nil {
		return nil, &media.Error{
			Code: media.CodeNoTrack, Op: "mp4.trak.missing", File: path,
			Offset: int64(trak.Info.Offset), Msg: "trak lacks tkhd/mdhd/hdlr",
		}
	}
	tkhdBox, err := unmarshalNode(f, tkhdNode)
	if err != nil {
		return nil, err
	}
	tkhd, ok := tkhdBox.(*mp4.Tkhd)
	if !ok {
		return nil, media.New(media.CodeInvalidBox, "mp4.tkhd", int64(tkhdNode.Info.Offset), "not a tkhd box")
	}
	mdhdBox, err := unmarshalNode(f, mdhdNode)
	if err != nil {
		return nil, err
	}
	mdhd, ok := mdhdBox.(*mp4.Mdhd)
	if !ok {
		return nil, media.New(media.CodeInvalidBox, "mp4.mdhd", int64(mdhdNode.Info.Offset), "not an mdhd box")
	}
	hdlrBox, err := unmarshalNode(f, hdlrNode)
	if err != nil {
		return nil, err
	}
	hdlr, ok := hdlrBox.(*mp4.Hdlr)
	if !ok {
		return nil, media.New(media.CodeInvalidBox, "mp4.hdlr", int64(hdlrNode.Info.Offset), "not an hdlr box")
	}
	track := &Track{ID: tkhd.TrackID, Timescale: mdhd.Timescale}
	switch string(hdlr.HandlerType[:]) {
	case "vide":
		track.Kind = KindVideo
	case "soun":
		track.Kind = KindAudio
	default:
		return nil, nil // data/subtitle tracks do not participate in replay
	}
	if track.Timescale == 0 {
		return nil, media.New(media.CodeInvalidBox, "mp4.mdhd", int64(mdhdNode.Info.Offset), "zero timescale")
	}
	if rot, ok := rotationFromMatrix(tkhd.Matrix); ok {
		track.Meta = &Meta{Rotation: &Rotation{Degrees: rot}}
	}
	track.Width = uint16(tkhd.GetWidth())
	track.Height = uint16(tkhd.GetHeight())

	minf := mdia.child("minf")
	if minf == nil {
		return nil, media.New(media.CodeNoTrack, "mp4.minf", int64(trak.Info.Offset), "mdia lacks minf")
	}
	stbl := minf.child("stbl")
	if stbl == nil {
		return nil, media.New(media.CodeNoTrack, "mp4.stbl", int64(minf.Info.Offset), "minf lacks stbl")
	}
	stsd := stbl.child("stsd")
	if stsd == nil || len(stsd.Children) == 0 {
		return nil, media.New(media.CodeNoTrack, "mp4.stsd", int64(stbl.Info.Offset), "stbl lacks sample description")
	}
	entry := stsd.Children[0]
	rawEntry, err := entry.raw(f)
	if err != nil {
		return nil, err
	}
	track.StsdRaw = rawEntry
	track.Codec = entry.Info.Type.String()
	switch track.Kind {
	case KindVideo:
		box, err := unmarshalNode(f, entry)
		if err != nil {
			return nil, err
		}
		vse, ok := box.(*mp4.VisualSampleEntry)
		if !ok {
			return nil, media.New(media.CodeInvalidBox, "mp4.stsd", int64(entry.Info.Offset), "unexpected video sample entry")
		}
		track.Width = vse.Width
		track.Height = vse.Height
		if track.Meta == nil {
			track.Meta = &Meta{}
		}
		if err := parseVisualMeta(f, entry, track); err != nil {
			return nil, err
		}
	case KindAudio:
		box, err := unmarshalNode(f, entry)
		if err != nil {
			return nil, err
		}
		ase, ok := box.(*mp4.AudioSampleEntry)
		if !ok {
			return nil, media.New(media.CodeInvalidBox, "mp4.stsd", int64(entry.Info.Offset), "unexpected audio sample entry")
		}
		track.ChannelCount = ase.ChannelCount
		track.SampleRate = ase.SampleRate >> 16
	}

	table, err := parseSampleTable(f, path, track, stbl)
	if err != nil {
		return nil, err
	}
	ticks := int64(0)
	for i := range table.sizes {
		dur := table.durs[i]
		rawStart := media.FromTicks(ticks, track.Timescale)
		s := Sample{
			Number:   i + 1,
			DTS:      rawStart,
			Duration: media.FromTicks(int64(dur), track.Timescale),
			PTS:      rawStart,
			Offset:   table.offsets[i],
			Size:     table.sizes[i],
			Keyframe: table.key[i],
		}
		if len(table.ctts) > i {
			s.PTS = rawStart.Add(media.FromTicks(table.ctts[i], track.Timescale))
		}
		ticks += int64(dur)
		track.Samples = append(track.Samples, s)
	}
	return track, nil
}

// parseVisualMeta reads optional colr and clap boxes. Unsupported colour
// types (prof/rICC) leave the colour descriptor absent rather than guessed.
func parseVisualMeta(f *os.File, entry *boxNode, track *Track) error {
	for _, c := range entry.Children {
		switch c.Info.Type.String() {
		case "colr":
			buf, err := c.payload(f)
			if err != nil {
				return err
			}
			if col, ok := parseColr(buf); ok {
				track.Meta.Color = col
			}
		case "clap":
			buf, err := c.payload(f)
			if err != nil {
				return err
			}
			if crop, ok := parseClap(buf, track.Width, track.Height); ok {
				track.Meta.Crop = crop
			}
		}
	}
	return nil
}

func parseColr(buf []byte) (*Color, bool) {
	if len(buf) < 11 || string(buf[:4]) != "nclx" {
		return nil, false
	}
	return &Color{
		Primaries: binary.BigEndian.Uint16(buf[4:6]),
		Transfer:  binary.BigEndian.Uint16(buf[6:8]),
		Matrix:    binary.BigEndian.Uint16(buf[8:10]),
		FullRange: buf[10]&0x80 != 0,
	}, true
}

// parseClap converts a clean-aperture box into symmetric margins. The
// aperture is centred on the coded picture unless an offset is present.
func parseClap(buf []byte, trackW, trackH uint16) (*Crop, bool) {
	if len(buf) < 32 {
		return nil, false
	}
	read := func(i int) (int64, int64) {
		n := int64(int32(binary.BigEndian.Uint32(buf[i*8:])))
		d := int64(int32(binary.BigEndian.Uint32(buf[i*8+4:])))
		if d == 0 {
			return 0, 1
		}
		return n, d
	}
	wn, wd := read(0)
	hn, hd := read(1)
	xoffN, xoffD := read(2)
	yoffN, yoffD := read(3)
	if wd <= 0 || hd <= 0 {
		return nil, false
	}
	apertureW := wn / wd
	apertureH := hn / hd
	if apertureW <= 0 || apertureH <= 0 || apertureW > int64(trackW) || apertureH > int64(trackH) {
		return nil, false
	}
	left := (int64(trackW)-apertureW)/2 + divTrunc(xoffN, xoffD)
	top := (int64(trackH)-apertureH)/2 + divTrunc(yoffN, yoffD)
	if left < 0 {
		left = 0
	}
	if top < 0 {
		top = 0
	}
	right := int64(trackW) - left - apertureW
	bottom := int64(trackH) - top - apertureH
	if right < 0 || bottom < 0 {
		return nil, false
	}
	return &Crop{Left: uint32(left), Top: uint32(top), Right: uint32(right), Bottom: uint32(bottom)}, true
}

func divTrunc(n, d int64) int64 {
	if d == 0 {
		return 0
	}
	return n / d
}

func rotationFromMatrix(m [9]int32) (int, bool) {
	const one = 1 << 16
	switch {
	case m[0] == one && m[4] == one && m[1] == 0 && m[3] == 0:
		return 0, true
	case m[0] == 0 && m[1] == one && m[3] == -one && m[4] == 0:
		return 90, true
	case m[0] == -one && m[4] == -one && m[1] == 0 && m[3] == 0:
		return 180, true
	case m[0] == 0 && m[1] == -one && m[3] == one && m[4] == 0:
		return 270, true
	default:
		return 0, false
	}
}

func parseSampleTable(f *os.File, path string, track *Track, stbl *boxNode) (*mp4SampleTable, error) {
	tbl := &mp4SampleTable{}
	stszNode := stbl.child("stsz")
	sttsNode := stbl.child("stts")
	stscNode := stbl.child("stsc")
	if stszNode == nil || sttsNode == nil || stscNode == nil {
		return nil, media.New(media.CodeNoTrack, "mp4.stbl", int64(stbl.Info.Offset), "sample table is incomplete")
	}
	stszBox, err := unmarshalNode(f, stszNode)
	if err != nil {
		return nil, err
	}
	stsz, ok := stszBox.(*mp4.Stsz)
	if !ok {
		return nil, media.New(media.CodeInvalidBox, "mp4.stsz", int64(stszNode.Info.Offset), "not an stsz box")
	}
	count := int(stsz.SampleCount)
	if count == 0 {
		return nil, media.New(media.CodeInvalidBox, "mp4.stsz", int64(stszNode.Info.Offset), "zero samples")
	}
	tbl.sizes = make([]uint32, count)
	if stsz.SampleSize != 0 {
		for i := range tbl.sizes {
			tbl.sizes[i] = stsz.SampleSize
		}
	} else {
		if len(stsz.EntrySize) < count {
			return nil, media.New(media.CodeInvalidBox, "mp4.stsz", int64(stszNode.Info.Offset), "sample size table too short")
		}
		copy(tbl.sizes, stsz.EntrySize[:count])
	}

	sttsBox, err := unmarshalNode(f, sttsNode)
	if err != nil {
		return nil, err
	}
	stts, ok := sttsBox.(*mp4.Stts)
	if !ok {
		return nil, media.New(media.CodeInvalidBox, "mp4.stts", int64(sttsNode.Info.Offset), "not an stts box")
	}
	tbl.durs = make([]uint32, count)
	idx := 0
	for _, e := range stts.Entries {
		for k := uint32(0); k < e.SampleCount && idx < count; k++ {
			tbl.durs[idx] = e.SampleDelta
			idx++
		}
	}
	// Remaining entries stay zero; repair treats them as missing timestamps.

	stscBox, err := unmarshalNode(f, stscNode)
	if err != nil {
		return nil, err
	}
	stsc, ok := stscBox.(*mp4.Stsc)
	if !ok {
		return nil, media.New(media.CodeInvalidBox, "mp4.stsc", int64(stscNode.Info.Offset), "not an stsc box")
	}
	entries := append([]mp4.StscEntry(nil), stsc.Entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].FirstChunk < entries[j].FirstChunk })
	if len(entries) == 0 {
		return nil, media.New(media.CodeInvalidBox, "mp4.stsc", int64(stscNode.Info.Offset), "no chunk mapping")
	}
	chunkOffsets, err := chunkOffsetsOf(f, stbl)
	if err != nil {
		return nil, err
	}
	tbl.offsets = make([]int64, count)
	sample := 0
	for ci, chunkOff := range chunkOffsets {
		chunkNum := uint32(ci + 1)
		entry := entries[0]
		for _, e := range entries {
			if e.FirstChunk <= chunkNum {
				entry = e
			}
		}
		off := chunkOff
		for k := uint32(0); k < entry.SamplesPerChunk && sample < count; k++ {
			tbl.offsets[sample] = off
			off += int64(tbl.sizes[sample])
			sample++
		}
		if sample >= count {
			break
		}
	}
	if sample < count {
		return nil, media.New(media.CodeInvalidBox, "mp4.stsc", int64(stscNode.Info.Offset),
			fmt.Sprintf("%d of %d samples mapped to chunks", sample, count))
	}

	tbl.key = make([]bool, count)
	if stssNode := stbl.child("stss"); stssNode != nil {
		stssBox, err := unmarshalNode(f, stssNode)
		if err != nil {
			return nil, err
		}
		stss, ok := stssBox.(*mp4.Stss)
		if !ok {
			return nil, media.New(media.CodeInvalidBox, "mp4.stss", int64(stssNode.Info.Offset), "not an stss box")
		}
		for _, n := range stss.SampleNumber {
			if n >= 1 && int(n) <= count {
				tbl.key[n-1] = true
			}
		}
	} else {
		// No stss: every sample is a random-access point. Audio packets are
		// always independently decodable, so this is correct for both kinds.
		for i := range tbl.key {
			tbl.key[i] = true
		}
	}

	if cttsNode := stbl.child("ctts"); cttsNode != nil {
		cttsBox, err := unmarshalNode(f, cttsNode)
		if err != nil {
			return nil, err
		}
		ctts, ok := cttsBox.(*mp4.Ctts)
		if !ok {
			return nil, media.New(media.CodeInvalidBox, "mp4.ctts", int64(cttsNode.Info.Offset), "not a ctts box")
		}
		tbl.ctts = make([]int64, 0, count)
		for _, e := range ctts.Entries {
			for k := uint32(0); k < e.SampleCount && len(tbl.ctts) < count; k++ {
				tbl.ctts = append(tbl.ctts, ctts.GetSampleOffset(len(tbl.ctts)))
			}
		}
	}
	return tbl, nil
}

func chunkOffsetsOf(f *os.File, stbl *boxNode) ([]int64, error) {
	if n := stbl.child("stco"); n != nil {
		box, err := unmarshalNode(f, n)
		if err != nil {
			return nil, err
		}
		stco, ok := box.(*mp4.Stco)
		if !ok {
			return nil, media.New(media.CodeInvalidBox, "mp4.stco", int64(n.Info.Offset), "not an stco box")
		}
		out := make([]int64, len(stco.ChunkOffset))
		for i, v := range stco.ChunkOffset {
			out[i] = int64(v)
		}
		return out, nil
	}
	if n := stbl.child("co64"); n != nil {
		box, err := unmarshalNode(f, n)
		if err != nil {
			return nil, err
		}
		co64, ok := box.(*mp4.Co64)
		if !ok {
			return nil, media.New(media.CodeInvalidBox, "mp4.co64", int64(n.Info.Offset), "not a co64 box")
		}
		out := make([]int64, len(co64.ChunkOffset))
		for i, v := range co64.ChunkOffset {
			if v > uint64(1)<<62 {
				return nil, media.New(media.CodeInvalidBox, "mp4.co64", int64(n.Info.Offset), "chunk offset out of range")
			}
			out[i] = int64(v)
		}
		return out, nil
	}
	return nil, media.New(media.CodeNoTrack, "mp4.stco", int64(stbl.Info.Offset), "no chunk offset table")
}

// ReadBytes reads a byte range from path; used by checks that compare sample
// payloads against their declared offsets.
func ReadBytes(path string, offset int64, size uint32) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, media.Wrap(err, media.CodeIO, "read.open", -1)
	}
	defer f.Close()
	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return nil, media.Wrap(err, media.CodeIO, "read.range", offset)
	}
	return buf, nil
}
