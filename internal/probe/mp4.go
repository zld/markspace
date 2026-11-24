package probe

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"

	"archive/internal/fault"
	"archive/internal/model"
)

// rawBox is one parsed top-level/recursive box header plus its byte range.
type rawBox struct {
	Type   mp4.BoxType
	Offset uint64
	Size   uint64
	Header uint64
	Depth  int
}

// walkBoxes parses the box tree defensively. Unlike mp4.ReadBoxStructure it
// never trusts a declared size: a box that claims more bytes than remain is
// reported as a truncated box at its real offset and the walk stops, which is
// what a damaged archive needs rather than an opaque parse error.
func walkBoxes(data []byte, faults *fault.List) []rawBox {
	var out []rawBox
	var walk func(off, end uint64, depth int, path []mp4.BoxType)
	walk = func(off, end uint64, depth int, path []mp4.BoxType) {
		for off < end {
			if end-off < 8 {
				faults.Add(fault.New(fault.TruncatedBox, off, "trailing %d bytes cannot hold a box header", end-off).
					WithRecoverable(true))
				return
			}
			size := uint64(binary.BigEndian.Uint32(data[off : off+4]))
			typ := mp4.BoxType{data[off+4], data[off+5], data[off+6], data[off+7]}
			header := uint64(8)
			switch size {
			case 0:
				// Extends to the end of the enclosing range.
				size = end - off
			case 1:
				if end-off < 16 {
					faults.Add(fault.New(fault.TruncatedBox, off, "large box header cut short for %s", typ).
						WithRecoverable(true))
					return
				}
				size = binary.BigEndian.Uint64(data[off+8 : off+16])
				header = 16
			}
			if size < header {
				faults.Add(fault.New(fault.InvalidLength, off, "box %s declares size %d below its header", typ, size).
					WithRecoverable(false))
				return
			}
			if off+size > end {
				// A box that claims absurdly more than the file could ever
				// hold (for example because its 32-bit size field was
				// rewritten) is an invalid length. A box that merely runs past
				// the remaining bytes is a truncation. The remux-able media is
				// the same either way; the codes let a report tell an operator
				// whether to look for a shortened file or a corrupted header.
				// A declared size far beyond any plausible file (>= 1 MiB
				// past a file that is itself below that) is a corrupted
				// length field rather than a shortened file. Genuine
				// truncation overshoots by the cut amount, not by orders of
				// magnitude.
				code := fault.TruncatedBox
				if size > uint64(len(data)) && size >= 1<<20 {
					code = fault.InvalidLength
				}
				faults.Add(fault.New(code, off, "box %s declares %d bytes but only %d remain", typ, size, end-off).
					WithRecoverable(true))
				out = append(out, rawBox{Type: typ, Offset: off, Size: end - off, Header: header, Depth: depth})
				return
			}
			out = append(out, rawBox{Type: typ, Offset: off, Size: size, Header: header, Depth: depth})
			if isContainerBox(typ) {
				walk(off+header, off+size, depth+1, append(path, typ))
			}
			off += size
		}
	}
	walk(0, uint64(len(data)), 0, nil)
	return out
}

// isContainerBox lists the plain containers whose payload is a box tree. A
// sample entry is handled separately because its children start after a fixed
// header that depends on the entry kind.
func isContainerBox(t mp4.BoxType) bool {
	switch t {
	case mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(),
		mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeDinf(),
		mp4.BoxTypeEdts(), mp4.BoxTypeMvex(), mp4.BoxTypeMoof(),
		mp4.BoxTypeTraf(), mp4.BoxTypeMfra(), mp4.BoxTypeUdta():
		return true
	}
	return false
}

// boxAt returns the first box of the given type inside the given range.
func findBox(boxes []rawBox, t mp4.BoxType, parentOff, parentSize uint64, depth int) *rawBox {
	for i := range boxes {
		b := &boxes[i]
		if b.Type == t && b.Offset >= parentOff && b.Offset < parentOff+parentSize && b.Depth == depth {
			return b
		}
	}
	return nil
}

// ProbeMP4 builds the track list for an MP4 source.
func ProbeMP4(src Source) (*File, error) {
	f := &File{Container: model.ContainerMP4, Path: src.Name, Size: uint64(len(src.Data)), Info: map[string]string{}}
	boxes := walkBoxes(src.Data, &f.Faults)
	if len(boxes) == 0 {
		return nil, fmt.Errorf("probe: %s has no parsable boxes", src.Name)
	}
	// Brand identification.
	if ftyp := findBox(boxes, mp4.BoxTypeFtyp(), 0, uint64(len(src.Data)), 0); ftyp != nil {
		payload := payloadOf(src.Data, *ftyp)
		if len(payload) >= 4 {
			f.Info["major_brand"] = string(payload[0:4])
		}
	}
	moov := findBox(boxes, mp4.BoxTypeMoov(), 0, uint64(len(src.Data)), 0)
	if moov == nil {
		return nil, fmt.Errorf("probe: %s has no moov box; metadata is unrecoverable", src.Name)
	}
	mvhd := findBox(boxes, mp4.BoxTypeMvhd(), moov.Offset, moov.Size, 1)
	if mvhd != nil {
		var box mp4.Mvhd
		if err := unmarshalBox(src.Data, *mvhd, &box); err == nil {
			f.Info["movie_timescale"] = fmt.Sprintf("%d", box.Timescale)
		}
	}
	// Each trak is one track.
	for _, b := range boxes {
		if b.Type != mp4.BoxTypeTrak() || b.Depth != 1 {
			continue
		}
		t, err := probeTrak(src.Data, boxes, b, &f.Faults)
		if err != nil {
			f.Faults.Add(fault.New(fault.InvalidLength, b.Offset, "track parse failed: %v", err).WithRecoverable(true))
			continue
		}
		if t != nil {
			f.Tracks = append(f.Tracks, t)
		}
	}
	if len(f.Tracks) == 0 {
		return nil, fmt.Errorf("probe: %s has no usable tracks", src.Name)
	}
	return f, nil
}

func payloadOf(data []byte, b rawBox) []byte {
	if b.Offset+b.Header > uint64(len(data)) {
		return nil
	}
	return data[b.Offset+b.Header : b.Offset+b.Size]
}

// unmarshalBox decodes a parsed box payload with go-mp4. Failure is non-fatal
// for the probe: metadata is best-effort, essence extraction uses the raw
// offsets.
func unmarshalBox(data []byte, b rawBox, dst mp4.IBox) error {
	payload := payloadOf(data, b)
	if len(payload) == 0 {
		return io.ErrUnexpectedEOF
	}
	_, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), dst, mp4.Context{})
	return err
}

func probeTrak(data []byte, boxes []rawBox, trak rawBox, faults *fault.List) (*model.Track, error) {
	t := &model.Track{
		Container: model.ContainerMP4,
		Source:    "mp4",
		Index:     model.IndexInfo{},
	}
	var tkhd mp4.Tkhd
	tkhdBox := findBox(boxes, mp4.BoxTypeTkhd(), trak.Offset, trak.Size, 2)
	if tkhdBox == nil {
		return nil, fmt.Errorf("missing tkhd")
	}
	if err := unmarshalBox(data, *tkhdBox, &tkhd); err != nil {
		return nil, fmt.Errorf("tkhd: %w", err)
	}
	t.TrackID = tkhd.TrackID
	t.RotationDeg = rotationFromMatrix(tkhd.Matrix)
	t.Key = TrackKey(model.ContainerMP4, t.TrackID)

	mdia := findBox(boxes, mp4.BoxTypeMdia(), trak.Offset, trak.Size, 2)
	if mdia == nil {
		return nil, fmt.Errorf("missing mdia")
	}
	var hdlr mp4.Hdlr
	if hb := findBox(boxes, mp4.BoxTypeHdlr(), mdia.Offset, mdia.Size, 3); hb != nil {
		_ = unmarshalBox(data, *hb, &hdlr)
	}
	t.Kind = kindFromHandler(hdlr.HandlerType)

	var mdhd mp4.Mdhd
	if mb := findBox(boxes, mp4.BoxTypeMdhd(), mdia.Offset, mdia.Size, 3); mb != nil {
		if err := unmarshalBox(data, *mb, &mdhd); err != nil {
			return nil, fmt.Errorf("mdhd: %w", err)
		}
		t.Timebase = model.Timebase{Timescale: mdhd.Timescale}
		t.DurationTick = mdhd.GetDuration()
	}
	if !t.Timebase.Valid() {
		// Fall back to the movie timescale; the timeline still has a clock.
		t.Timebase = model.Timebase{Timescale: 1000}
		faults.Add(fault.New(fault.MissingIndex, mdia.Offset, "mdhd timescale missing; using %d", t.Timebase.Timescale).
			WithTrack(t.Key, t.TrackID).WithRecoverable(true))
	}

	// Descriptive metadata and sample table live under minf/stbl.
	minf := findBox(boxes, mp4.BoxTypeMinf(), mdia.Offset, mdia.Size, 3)
	if minf == nil {
		return nil, fmt.Errorf("missing minf")
	}
	stbl := findBox(boxes, mp4.BoxTypeStbl(), minf.Offset, minf.Size, 4)
	if stbl == nil {
		return nil, fmt.Errorf("missing stbl")
	}
	probeSampleDescription(data, boxes, *stbl, t, faults)
	if err := probeSampleTable(data, boxes, *stbl, t, faults); err != nil {
		return nil, err
	}
	// The edit list shifts presentation; the explicit timeline records the
	// shift so the demuxer can stay in media time.
	if edts := findBox(boxes, mp4.BoxTypeEdts(), trak.Offset, trak.Size, 2); edts != nil {
		if elst := findBox(boxes, mp4.BoxTypeElst(), edts.Offset, edts.Size, 3); elst != nil {
			var box mp4.Elst
			if err := unmarshalBox(data, *elst, &box); err == nil && len(box.Entries) > 0 {
				mediaTime := elstMediaTime(box)
				if mediaTime != 0 {
					t.EditMediaTime = mediaTime
				}
			}
		}
	}
	t.Required = t.Kind == model.KindVideo || t.Kind == model.KindAudio
	return t, nil
}

func kindFromHandler(h [4]byte) model.TrackKind {
	switch string(h[:]) {
	case "vide":
		return model.KindVideo
	case "soun":
		return model.KindAudio
	default:
		return model.KindData
	}
}

// probeSampleDescription reads the first sample entry for codec, visual
// rotation-adjacent metadata (colr/clap/pasp) and audio parameters.
func probeSampleDescription(data []byte, boxes []rawBox, stbl rawBox, t *model.Track, faults *fault.List) {
	stsd := findBox(boxes, mp4.BoxTypeStsd(), stbl.Offset, stbl.Size, 5)
	if stsd == nil {
		t.MissingOptional = append(t.MissingOptional, "stsd")
		return
	}
	// The sample entry starts after the stsd full-box header and entry count:
	// 8 bytes box header + 4 version/flags + 4 entry count = 16.
	entryOff := stsd.Offset + 16
	if entryOff+8 > stsd.Offset+stsd.Size || entryOff+8 > uint64(len(data)) {
		faults.Add(fault.New(fault.TruncatedBox, stsd.Offset, "stsd cut before its first entry").
			WithTrack(t.Key, t.TrackID).WithRecoverable(true))
		return
	}
	size := uint64(binary.BigEndian.Uint32(data[entryOff : entryOff+4]))
	entry := rawBox{Type: mp4.BoxType{data[entryOff+4], data[entryOff+5], data[entryOff+6], data[entryOff+7]}, Offset: entryOff, Size: size, Header: 8}
	if entry.Size < entry.Header || entryOff+entry.Size > stsd.Offset+stsd.Size {
		faults.Add(fault.New(fault.InvalidLength, entryOff, "sample entry size %d out of range", entry.Size).
			WithTrack(t.Key, t.TrackID).WithRecoverable(false))
		return
	}
	t.Codec = entry.Type.String()
	// Walk the sample entry's child boxes. Visual entries have a 78-byte
	// header (including the box header), audio entries 28; the child walk
	// starts exactly there.
	childStart := entry.Offset + entry.Header
	var visual bool
	switch entry.Type {
	case mp4.BoxTypeAvc1(), mp4.BoxTypeHev1(), mp4.BoxTypeHvc1(), mp4.BoxTypeMp4v(), mp4.BoxTypeEncv(),
		mp4.BoxTypeVp08(), mp4.BoxTypeVp09(), mp4.BoxTypeAv01():
		visual = true
		// VisualSampleEntry payload is 78 bytes; children follow the
		// 8-byte box header plus that payload.
		childStart = entry.Offset + 8 + 78
	case mp4.BoxTypeMp4a(), mp4.BoxTypeEnca(), mp4.BoxTypeOpus(), mp4.BoxTypeAC3():
		// AudioSampleEntry payload is 28 bytes.
		childStart = entry.Offset + 8 + 28
	}
	if !visual {
		// Audio/data tracks carry no picture colour; record the default with
		// its source so the mapper has a uniform field set.
		t.Color = description("bt709", "bt709", "bt709", "limited", "default")
		t.ColorClaims = claims(t.Color)
		t.MissingOptional = append(t.MissingOptional, "color")
		return
	}
	if childStart >= entry.Offset+entry.Size {
		return
	}
	child := walkBoxes(data[childStart:entry.Offset+entry.Size], faults)
	// Rebase the child boxes onto absolute offsets so they line up with the
	// rest of the file.
	for i := range child {
		child[i].Offset += childStart
	}
	findChild := func(typ mp4.BoxType) *rawBox {
		for i := range child {
			if child[i].Type == typ {
				return &child[i]
			}
		}
		return nil
	}
	if !visual {
		return
	}
	var colr mp4.Colr
	if cb := findChild(mp4.BoxTypeColr()); cb != nil {
		if err := unmarshalBox(data, *cb, &colr); err == nil {
			rng := "limited"
			if colr.FullRangeFlag {
				rng = "full"
			}
			t.Color = description(
				colorPrimariesName(colr.ColourPrimaries),
				colorTransferName(colr.TransferCharacteristics),
				colorMatrixName(colr.MatrixCoefficients),
				rng, "mp4")
			t.ColorClaims = claims(t.Color)
		} else {
			t.MissingOptional = append(t.MissingOptional, "colr")
		}
	} else {
		t.MissingOptional = append(t.MissingOptional, "colr")
	}
	if cb := findChild(mp4.StrToBoxType("clap")); cb != nil {
		payload := payloadOf(data, *cb)
		if len(payload) >= 32 {
			wN := binary.BigEndian.Uint32(payload[0:4])
			wD := binary.BigEndian.Uint32(payload[4:8])
			hN := binary.BigEndian.Uint32(payload[8:12])
			hD := binary.BigEndian.Uint32(payload[12:16])
			if wD != 0 && hD != 0 {
				t.Crop = &model.Crop{Left: 0, Top: 0, Right: int(wN / wD), Bottom: int(hN / hD)}
			}
		}
	}
	if t.Color.Primaries.Value == "" {
		// No colour box at all: record the container default with its
		// source so arbitration can use it without pretending it was read.
		t.Color = description("bt709", "bt709", "bt709", "limited", "default")
		t.ColorClaims = claims(t.Color)
		t.MissingOptional = append(t.MissingOptional, "color")
	}
}

// probeSampleTable builds fragments from stts/stsc/stsz/stco/stss. A missing
// or inconsistent table leaves Index.Complete false so recovery scans.
func probeSampleTable(data []byte, boxes []rawBox, stbl rawBox, t *model.Track, faults *fault.List) error {
	index := model.IndexInfo{OffsetBytes: stbl.Offset, Source: "stbl"}
	get := func(typ mp4.BoxType) *rawBox {
		return findBox(boxes, typ, stbl.Offset, stbl.Size, 5)
	}
	sttsB := get(mp4.BoxTypeStts())
	stszB := get(mp4.BoxTypeStsz())
	stscB := get(mp4.BoxTypeStsc())
	stcoB := get(mp4.BoxTypeStco())
	co64B := get(mp4.BoxTypeCo64())
	var stssB = get(mp4.BoxTypeStss())

	if sttsB == nil || stszB == nil || stscB == nil || (stcoB == nil && co64B == nil) {
		index.Present = false
		index.Complete = false
		index.Reason = "sample table incomplete"
		var missing []string
		if sttsB == nil {
			missing = append(missing, "stts")
		}
		if stszB == nil {
			missing = append(missing, "stsz")
		}
		if stscB == nil {
			missing = append(missing, "stsc")
		}
		if stcoB == nil && co64B == nil {
			missing = append(missing, "stco")
		}
		t.MissingOptional = append(t.MissingOptional, missing...)
		t.Index = index
		faults.Add(fault.New(fault.MissingIndex, stbl.Offset, "sample table missing %v", missing).
			WithTrack(t.Key, t.TrackID).WithRecoverable(true))
		return nil
	}

	var stts mp4.Stts
	if err := unmarshalBox(data, *sttsB, &stts); err != nil {
		return fmt.Errorf("stts: %w", err)
	}
	var stsz mp4.Stsz
	if err := unmarshalBox(data, *stszB, &stsz); err != nil {
		return fmt.Errorf("stsz: %w", err)
	}
	var stsc mp4.Stsc
	if err := unmarshalBox(data, *stscB, &stsc); err != nil {
		return fmt.Errorf("stsc: %w", err)
	}
	var offsets []uint64
	if stcoB != nil {
		var stco mp4.Stco
		if err := unmarshalBox(data, *stcoB, &stco); err != nil {
			return fmt.Errorf("stco: %w", err)
		}
		for _, o := range stco.ChunkOffset {
			offsets = append(offsets, uint64(o))
		}
	} else {
		var co64 mp4.Co64
		if err := unmarshalBox(data, *co64B, &co64); err != nil {
			return fmt.Errorf("co64: %w", err)
		}
		offsets = append(offsets, co64.ChunkOffset...)
	}
	syncSet := map[uint32]bool{}
	if stssB != nil {
		var stss mp4.Stss
		if err := unmarshalBox(data, *stssB, &stss); err == nil {
			for _, n := range stss.SampleNumber {
				syncSet[n] = true
			}
		}
	}

	frags, err := buildSampleFragments(stts, stsz, stsc, offsets, syncSet, t)
	// A positive edit media time shifts presentation: the sample table itself
	// always starts at decode time zero, so the explicit timeline must add
	// the offset back to reproduce the absolute start.
	if err == nil && t.EditMediaTime > 0 {
		for i := range frags {
			frags[i].StartTick += uint64(t.EditMediaTime)
		}
	}
	if err != nil {
		index.Present = true
		index.Complete = false
		index.Reason = err.Error()
		t.Index = index
		faults.Add(fault.New(fault.MissingIndex, stbl.Offset, "sample table inconsistent: %v", err).
			WithTrack(t.Key, t.TrackID).WithRecoverable(true))
		return nil
	}
	index.Present = true
	index.Complete = true
	index.Entries = len(frags)
	t.Index = index
	t.Fragments = frags
	t.StartTick = frags[0].StartTick
	end := uint64(0)
	for _, f := range frags {
		if f.EndTick() > end {
			end = f.EndTick()
		}
	}
	t.DurationTick = end - t.StartTick
	return nil
}

// buildSampleFragments converts the sample tables into absolute fragments.
// Any inconsistency that would make an offset ambiguous is reported rather
// than guessed.
func buildSampleFragments(stts mp4.Stts, stsz mp4.Stsz, stsc mp4.Stsc, chunks []uint64, syncSet map[uint32]bool, t *model.Track) ([]model.Fragment, error) {
	var durations []uint32
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			durations = append(durations, e.SampleDelta)
		}
	}
	var sizes []uint32
	if stsz.SampleSize != 0 {
		for i := uint32(0); i < stsz.SampleCount; i++ {
			sizes = append(sizes, stsz.SampleSize)
		}
	} else {
		sizes = stsz.EntrySize
	}
	count := len(sizes)
	if len(durations) != count {
		return nil, fmt.Errorf("%d durations for %d samples", len(durations), count)
	}
	if len(stsc.Entries) == 0 {
		return nil, fmt.Errorf("empty stsc")
	}
	type chunkRun struct {
		first    uint32
		perChunk uint32
	}
	var runs []chunkRun
	for _, e := range stsc.Entries {
		runs = append(runs, chunkRun{first: e.FirstChunk, perChunk: e.SamplesPerChunk})
	}
	samplesPerChunk := func(chunk uint32) (uint32, error) {
		idx := -1
		for i, r := range runs {
			if r.first <= chunk {
				idx = i
			}
		}
		if idx < 0 {
			return 0, fmt.Errorf("chunk %d before first stsc entry", chunk)
		}
		return runs[idx].perChunk, nil
	}

	var frags []model.Fragment
	sample := 0
	startTick := uint64(0)
	for ci, chunkOff := range chunks {
		chunk := uint32(ci + 1)
		per, err := samplesPerChunk(chunk)
		if err != nil {
			return nil, err
		}
		off := chunkOff
		for j := uint32(0); j < per && sample < count; j++ {
			f := model.Fragment{
				Seq:           sample,
				WrapperOffset: off,
				PayloadOffset: off,
				Size:          uint64(sizes[sample]),
				StartTick:     startTick,
				Duration:      uint64(durations[sample]),
				Sync:          syncSet[uint32(sample+1)],
			}
			frags = append(frags, f)
			startTick += uint64(durations[sample])
			off += uint64(sizes[sample])
			sample++
		}
	}
	if sample != count {
		return nil, fmt.Errorf("%d of %d samples placed", sample, count)
	}
	return frags, nil
}

// elstMediaTime returns the first edit's media time in its declared width.
func elstMediaTime(box mp4.Elst) int64 {
	e := box.Entries[0]
	if box.GetVersion() == 1 {
		return e.MediaTimeV1
	}
	return int64(e.MediaTimeV0)
}
