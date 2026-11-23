package ingest

import (
	"fmt"

	"mediapipe/internal/fault"
	"mediapipe/internal/limits"
	"mediapipe/internal/model"
)

// ParseMP4 parses an MP4 file into model tracks. A truncated box produces a
// recoverable fault with its byte offset; complete tracks remain available.
func ParseMP4(path string, errs *fault.Batch) ([]*model.Track, error) {
	data, ferr := readInput(path)
	if ferr != nil {
		errs.Add(ferr)
		return nil, ferr
	}
	top := scanTopBoxes(path, data, errs)
	var moov *rawBox
	for i := range top {
		if top[i].typ == "moov" {
			moov = &top[i]
		}
	}
	if moov == nil {
		ferr := fault.New(fault.KindInvalidInput, "no moov box found", path, -1, false)
		errs.Add(ferr)
		return nil, ferr
	}
	traks := findChildren(*moov, "trak", path, errs)
	if len(traks) > limits.MaxTracks {
		errs.Add(limits.Exceeded(path, "track count", moov.offset, len(traks), limits.MaxTracks))
		traks = traks[:limits.MaxTracks]
	}
	var tracks []*model.Track
	for ti := range traks {
		tr, ok := parseTrak(traks[ti], path, ti, errs)
		if ok {
			tracks = append(tracks, tr)
		}
	}
	return tracks, nil
}

func parseTrak(trak rawBox, path string, idx int, errs *fault.Batch) (*model.Track, bool) {
	tkhdBoxes := findChildren(trak, "tkhd", path, errs)
	mdhdBoxes := findChildren(trak, "mdhd", path, errs)
	hdlrBoxes := findChildren(trak, "hdlr", path, errs)
	stblBoxes := findChildren(trak, "stbl", path, errs)
	if len(tkhdBoxes) == 0 || len(mdhdBoxes) == 0 || len(hdlrBoxes) == 0 ||
		len(stblBoxes) == 0 {
		errs.Add(fault.New(fault.KindTruncated,
			"trak missing required sub-box", path, trak.offset, true))
		return nil, false
	}
	trackID, w, h, rot := parseTkhd(tkhdBoxes[0])
	timescale, _ := parseMdhd(mdhdBoxes[0])
	handler := parseHdlr(hdlrBoxes[0])
	kind := model.KindVideo
	if handler == "soun" {
		kind = model.KindAudio
	}

	tr := &model.Track{
		ID:         fmt.Sprintf("%s:trak-%d-%d", path, idx, trackID),
		Kind:       kind,
		Timescale:  timescale,
		Container:  model.ContainerMP4,
		SourceFile: path,
		TrackOff:   trak.offset,
		Width:      w,
		Height:     h,
		Rotation:   rot,
	}

	// Color declarations and clean aperture from the sample description.
	for _, c := range findChildren(stblBoxes[0], "colr", path, errs) {
		tr.Color.Declarations = append(tr.Color.Declarations, parseColr(c, path, errs))
	}
	for _, c := range findChildren(stblBoxes[0], "clap", path, errs) {
		if crop := parseClap(c, w, h); crop != nil {
			tr.Crop = crop
		}
	}

	tables := readStbl(stblBoxes[0], path, errs)
	tr.Samples = buildSamples(tables, timescale, path, errs, kind)
	if len(tr.Samples) > limits.MaxSamplesPerTrack {
		errs.Add(limits.Exceeded(path, "sample count", trak.offset,
			len(tr.Samples), limits.MaxSamplesPerTrack))
		tr.Samples = tr.Samples[:limits.MaxSamplesPerTrack]
	}

	// Edit-list media start shifts every timestamp; never below zero.
	if elsts := findChildren(trak, "elst", path, errs); len(elsts) > 0 && timescale > 0 {
		startUnits := parseElst(elsts[0])
		startNanos := startUnits * 1_000_000_000 / int64(timescale)
		for i := range tr.Samples {
			tr.Samples[i].DTS += startNanos
			tr.Samples[i].PTS += startNanos
			if tr.Samples[i].DTS < 0 {
				tr.Samples[i].DTS = 0
			}
			if tr.Samples[i].PTS < 0 {
				tr.Samples[i].PTS = 0
			}
		}
	}
	tr.SortPresentation()
	return tr, true
}

func readStbl(stbl rawBox, path string, errs *fault.Batch) stblTables {
	var t stblTables
	t.keys = map[int]bool{}
	for _, c := range childBoxes(stbl, path, errs) {
		switch c.typ {
		case "stts":
			t.deltas = parseStts(c)
		case "ctts":
			t.ctsOff = parseCtts(c)
		case "stsz":
			t.sizes = parseStsz(c)
		case "stco":
			t.offsets = parseStco(c, false)
		case "co64":
			t.offsets = parseStco(c, true)
		case "stss":
			t.keys = parseStss(c)
		case "stsc":
			validateStsc(c, path, errs)
		}
	}
	n := len(t.sizes)
	// One sample per chunk: chunk offset i maps directly to sample i. A
	// mismatch means a truncated table; the fault keeps the byte offset and
	// buildSamples tolerates the missing entries with -1 payload offsets.
	if len(t.offsets) != n {
		errs.Add(fault.New(fault.KindTruncated,
			fmt.Sprintf("chunk offset count %d != sample count %d", len(t.offsets), n),
			path, stbl.offset, true))
	}
	return t
}
