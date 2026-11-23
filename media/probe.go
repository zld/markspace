package media

import (
	"io"
	"sort"
)

// TrackInfo is the per-track probe result including its resolved sample table.
type TrackInfo struct {
	TrackID     uint32       `json:"track_id"`
	Handler     string       `json:"handler"`
	Codec       string       `json:"codec"`
	Language    string       `json:"language"`
	Timescale   uint32       `json:"timescale"`
	Duration    uint64       `json:"duration"`
	SampleCount uint32       `json:"sample_count"`
	Keyframes   []uint32     `json:"keyframes,omitempty"`
	HasEdits    bool         `json:"has_edits"`
	Edits       []EditEntry  `json:"edits,omitempty"`
	Table       *SampleTable `json:"-"`
	SourceIndex int          `json:"source_index"`
	Raw         *rawTrack    `json:"-"`
}

// Probe is the structural summary of an MP4/MOV file.
type Probe struct {
	Path      string       `json:"path"`
	Size      int64        `json:"size"`
	Brand     string       `json:"brand"`
	Timescale uint32       `json:"timescale"`
	Duration  uint64       `json:"duration"`
	FastStart bool         `json:"fast_start"`
	Tracks    []*TrackInfo `json:"tracks"`
	TopBoxes  []Box        `json:"top_boxes"`
	Unknown   []string     `json:"unknown_boxes,omitempty"`
	SampleSum uint64       `json:"sample_sum"`
	EditCount int          `json:"edit_lists"`
	Movie     *rawMovie    `json:"-"`
}

var supportedTopBoxes = map[string]bool{
	"ftyp": true, "moov": true, "mdat": true, "free": true, "skip": true,
	"wide": true, "uuid": true, "moof": true, "mfra": true, "styp": true,
	"sidx": true, "meta": true, "pdin": true, "udta": true,
}

var requiredTopBoxes = []string{"ftyp", "moov", "mdat"}

// ProbeFile parses and summarizes an MP4/MOV file. It enforces the input size
// cap, classifies truncation and bounds errors, and rejects unknown required
// boxes explicitly rather than silently ignoring them.
func ProbeFile(r io.ReadSeeker, size int64, path string) (*Probe, error) {
	if size <= 0 {
		return nil, NewError(ClassTruncated, "probe", 0, "empty input")
	}
	if size > MaxInputSize {
		return nil, NewError(ClassSizeOverflow, "probe", 0, "input size %d exceeds limit %d", size, MaxInputSize)
	}
	top, err := ScanBoxes(r, size)
	if err != nil {
		return nil, err
	}
	p := &Probe{Path: path, Size: size, TopBoxes: top}
	seen := make(map[string]bool, len(top))
	mdatAt, moovAt := int64(-1), int64(-1)
	for _, b := range top {
		seen[b.Type] = true
		if !supportedTopBoxes[b.Type] {
			p.Unknown = append(p.Unknown, b.Type)
		}
		switch b.Type {
		case "mdat":
			if mdatAt < 0 {
				mdatAt = b.Offset
			}
		case "moov":
			if moovAt < 0 {
				moovAt = b.Offset
			}
		}
	}
	for _, req := range requiredTopBoxes {
		if !seen[req] {
			return nil, NewError(ClassUnsupported, "probe", 0, "required top-level box %q missing", req)
		}
	}
	if len(p.Unknown) > 0 {
		sort.Strings(p.Unknown)
		return nil, NewError(ClassUnsupported, "probe", 0, "unknown top-level box %q", p.Unknown[0])
	}
	p.FastStart = moovAt >= 0 && (mdatAt < 0 || moovAt < mdatAt)

	mv, err := parseMovie(r, size)
	if err != nil {
		return nil, err
	}
	p.Movie = mv
	p.Brand = string(mv.Ftyp.MajorBrand[:])
	p.Timescale = mv.Mvhd.Timescale
	p.Duration = mv.Mvhd.GetDuration()
	for i, rt := range mv.Tracks {
		ti, err := buildTrackInfo(r, rt, i)
		if err != nil {
			return nil, err
		}
		p.Tracks = append(p.Tracks, ti)
		p.SampleSum += uint64(ti.SampleCount)
		if len(ti.Edits) > 0 {
			p.EditCount++
		}
	}
	return p, nil
}

func buildTrackInfo(r io.ReadSeeker, rt *rawTrack, idx int) (*TrackInfo, error) {
	mdhd := rt.Mdhd
	ti := &TrackInfo{
		TrackID:     rt.Tkhd.TrackID,
		Handler:     handlerName(rt.Hdlr),
		Codec:       rt.Codec,
		Language:    languageString(mdhd.Language),
		Timescale:   mdhd.Timescale,
		Duration:    mdhd.GetDuration(),
		HasEdits:    rt.Elst != nil && len(rt.Elst.Entries) > 0,
		SourceIndex: idx,
		Raw:         rt,
	}
	id := ti.TrackID
	if id == 0 {
		return nil, NewError(ClassInvalidSample, "probe.track", rt.Trak.Offset, "track id 0 is invalid")
	}
	if ti.Timescale == 0 {
		return nil, NewError(ClassTimebase, "probe.track", rt.Trak.Offset, "zero timescale").WithTrack(id)
	}
	if rt.Elst != nil {
		for i := range rt.Elst.Entries {
			ti.Edits = append(ti.Edits, EditEntry{
				SegmentDuration: rt.Elst.GetSegmentDuration(i),
				MediaTime:       rt.Elst.GetMediaTime(i),
				Rate:            int32(rt.Elst.Entries[i].MediaRateInteger),
			})
		}
	}
	st, err := buildSampleTable(rt)
	if err != nil {
		if ce, ok := AsError(err); ok {
			return nil, ce.WithTrack(id)
		}
		return nil, err
	}
	if err := validateSamples(st); err != nil {
		return nil, err
	}
	for i := range st.Sync {
		if st.Sync[i] {
			ti.Keyframes = append(ti.Keyframes, uint32(i+1))
		}
	}
	ti.Table = st
	ti.SampleCount = st.Count
	return ti, nil
}
