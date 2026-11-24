package container

import (
	"errors"
	"fmt"
	"os"

	"github.com/abema/go-mp4"

	"mediacheck/internal/media"
)

// ErrUnparseable marks a file that cannot be parsed at all. The CLI maps it
// onto exit code 2.
var ErrUnparseable = errors.New("unparseable media")

// SampleIndex is the decoded stbl content for one track. Missing boxes leave
// the corresponding present flag false; the scan never fabricates entries.
type SampleIndex struct {
	SttsPresent bool            `json:"stts_present"`
	StssPresent bool            `json:"stss_present"`
	StscPresent bool            `json:"stsc_present"`
	StszPresent bool            `json:"stsz_present"`
	StcoPresent bool            `json:"stco_present"`
	Deltas      []mp4.SttsEntry `json:"-"`
	SyncSamples []uint32        `json:"sync_samples"`
	SampleCount uint32          `json:"sample_count"`
	// Undecidable is set when a required table box is missing, truncated or
	// internally inconsistent; boundaries derived afterwards stay inferred.
	Undecidable bool `json:"undecidable"`
	// TruncatedPayload means a table box header survived but its payload was
	// cut short during decode.
	TruncatedPayload bool   `json:"truncated_payload"`
	Reason           string `json:"reason,omitempty"`
}

// TrackModel is everything the scan establishes for one trak.
type TrackModel struct {
	TrackID     uint32           `json:"track_id"`
	HandlerType string           `json:"handler_type"`
	Timescale   uint32           `json:"timescale"`
	Duration    uint64           `json:"duration"`
	Width       uint32           `json:"width"`
	Height      uint32           `json:"height"`
	Index       SampleIndex      `json:"index"`
	Edits       []media.Edit     `json:"edits,omitempty"`
	RawSamples  []media.RawSample `json:"-"`
}

// Kind maps handler_type onto the shared media.SampleKind taxonomy.
func (t *TrackModel) Kind() media.SampleKind {
	switch t.HandlerType {
	case "vide":
		return media.KindVideo
	case "soun":
		return media.KindAudio
	case "hint":
		return media.KindHint
	default:
		return media.KindUnknown
	}
}

// ScanResult is the full container analysis handed to the timeline builder
// and to damage localization.
type ScanResult struct {
	Path        string       `json:"path"`
	FileSize    int64        `json:"file_size"`
	MovieScale  uint32       `json:"movie_timescale"`
	Tracks      []TrackModel `json:"tracks"`
	Pre         *PreScan     `json:"-"`
	HasMoov     bool         `json:"has_moov"`
	// ParseIncomplete means go-mp4 stopped on a cut box; tracks decoded up
	// to that point are retained with undecidable tail indexes.
	ParseIncomplete bool `json:"parse_incomplete"`
}

// boxType returns the four-char type of the box owning a handle.
func boxType(h *mp4.ReadHandle) string {
	return h.BoxInfo.Type.String()
}

// pathHas reports whether the current box path contains the given type at
// any position.
func pathHas(h *mp4.ReadHandle, typ string) bool {
	for _, t := range h.Path {
		if t.String() == typ {
			return true
		}
	}
	return false
}

// Parse runs go-mp4 over an already pre-scanned file and decodes every
// supported track table. A file with no surviving moov is unparseable
// rather than an empty-but-valid asset.
func Parse(path string, pre *PreScan) (*ScanResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	res := &ScanResult{Path: path, FileSize: pre.FileSize, Pre: pre}

	_, err = mp4.ReadBoxStructure(f, func(h *mp4.ReadHandle) (interface{}, error) {
		typ := boxType(h)
		switch {
		case typ == "mvhd":
			if box, _, e := h.ReadPayload(); e == nil {
				if mv, ok := box.(*mp4.Mvhd); ok {
					res.MovieScale = mv.Timescale
				}
			}
			return nil, nil
		case typ == "trak":
			res.Tracks = append(res.Tracks, TrackModel{})
			slot := len(res.Tracks) - 1
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
			finalize := res.Tracks[slot]
			finalize.Index.Undecidable, finalize.Index.Reason = validateIndex(finalize.Index)
			res.Tracks[slot] = finalize
			return nil, nil
		case pathHas(h, "trak") && typ == "tkhd":
			return decodeTkhd(h, res)
		case pathHas(h, "trak") && typ == "mdhd":
			return decodeMdhd(h, res)
		case pathHas(h, "trak") && typ == "hdlr":
			return decodeHdlr(h, res)
		case pathHas(h, "trak") && typ == "elst":
			return decodeElst(h, res)
		case pathHas(h, "stbl") && (typ == "stts" || typ == "stss" || typ == "stsc" ||
			typ == "stsz" || typ == "stco" || typ == "co64"):
			return decodeStblBox(h, res)
		}
		return h.Expand()
	})
	if err != nil {
		// A cut moov/stbl still yields a partial, explicitly-undecidable
		// result when the pre-scan located a truncation; the pipeline then
		// localizes the tail range instead of refusing the whole file.
		if len(pre.Truncations) == 0 || len(res.Tracks) == 0 {
			return nil, fmt.Errorf("%w: %v", ErrUnparseable, err)
		}
		res.ParseIncomplete = true
	}
	// Any moov record (even a cut one) means the asset was a real MP4; a
	// file with none at all is unparseable.
	for _, b := range pre.Boxes {
		if b.Type == "moov" {
			res.HasMoov = true
		}
	}
	if !res.HasMoov {
		return nil, fmt.Errorf("%w: no moov box", ErrUnparseable)
	}
	return res, nil
}

// lastTrack returns a pointer to the TrackModel currently being expanded.
func lastTrack(res *ScanResult) *TrackModel {
	if len(res.Tracks) == 0 {
		return nil
	}
	return &res.Tracks[len(res.Tracks)-1]
}

func decodeTkhd(h *mp4.ReadHandle, res *ScanResult) (interface{}, error) {
	tm := lastTrack(res)
	if tm == nil {
		return nil, nil
	}
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, err
	}
	if tk, ok := box.(*mp4.Tkhd); ok {
		tm.TrackID = tk.TrackID
		tm.Duration = tk.GetDuration()
		tm.Width = tk.Width
		tm.Height = tk.Height
	}
	return nil, nil
}

func decodeMdhd(h *mp4.ReadHandle, res *ScanResult) (interface{}, error) {
	tm := lastTrack(res)
	if tm == nil {
		return nil, nil
	}
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, err
	}
	if md, ok := box.(*mp4.Mdhd); ok {
		tm.Timescale = md.Timescale
		if md.GetDuration() > tm.Duration {
			tm.Duration = md.GetDuration()
		}
	}
	return nil, nil
}

func decodeHdlr(h *mp4.ReadHandle, res *ScanResult) (interface{}, error) {
	tm := lastTrack(res)
	if tm == nil {
		return nil, nil
	}
	box, _, err := h.ReadPayload()
	if err != nil {
		return nil, err
	}
	if hd, ok := box.(*mp4.Hdlr); ok {
		tm.HandlerType = string(hd.HandlerType[:])
	}
	return nil, nil
}

// validateIndex enforces the rule that missing required tables make the
// index undecidable — never silently complete.
func validateIndex(idx SampleIndex) (bool, string) {
	if !idx.SttsPresent {
		return true, "stts missing: sample timing undecidable"
	}
	if !idx.StszPresent {
		return true, "stsz missing: sample sizes undecidable"
	}
	if !idx.StcoPresent && !idx.StscPresent {
		return true, "stsc/stco missing: sample locations undecidable"
	}
	return false, ""
}
