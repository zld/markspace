package evidence

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"

	"regress/internal/model"
	"regress/internal/timeline"
)

// ErrEmpty is returned when an evidence input has no samples to render.
var (
	errEmptyFrame = errors.New("frame has no pixels")
	errEmptyAudio = errors.New("audio block has no samples")
)

// Report is the complete offline evidence bundle for one run. Slices are
// rendered in deterministic (track, seq) order and JSON keys are sorted by
// the encoder, so identical inputs hash identically.
type Report struct {
	Frames         []FrameStats     `json:"frames"`
	Audio          []AudioStats     `json:"audio"`
	Alignment      []AlignmentError `json:"alignment"`
	MaxAlignError  int64            `json:"max_alignment_error_tick"`
	MissingEvents  int              `json:"missing_events"`
}

// Build walks the reconstructed tracks and aligned timeline and assembles
// the full evidence report. Video and audio samples are processed one at a
// time so peak per-task memory stays bounded by one frame/block.
func Build(tracks []model.Track, tl *timeline.Timeline) (*Report, error) {
	rep := &Report{}
	// Stable track order regardless of ingest slice ordering.
	ordered := append([]model.Track(nil), tracks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})
	for _, tr := range ordered {
		for _, s := range tr.Samples {
			switch tr.Kind {
			case model.KindVideo:
				fs, err := Frame(tr.ID, s)
				if err != nil {
					return nil, err
				}
				rep.Frames = append(rep.Frames, fs)
			case model.KindAudio:
				as, err := Audio(tr.ID, s)
				if err != nil {
					return nil, err
				}
				rep.Audio = append(rep.Audio, as)
			}
		}
	}
	rep.Alignment = Alignment(tl)
	maxErr, missing := MaxAbsAlignmentError(rep.Alignment)
	rep.MaxAlignError = int64(maxErr)
	rep.MissingEvents = missing
	return rep, nil
}

// Marshal serializes the report as deterministic pretty JSON: struct field
// order is fixed by the type and map keys (none are marshaled here) would be
// sorted by encoding/json anyway.
func (r *Report) Marshal() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, model.NewError(model.StageArtifact, "evidence-encode",
			"", "", 0, err)
	}
	return buf.Bytes(), nil
}

// Summary renders the short human-readable evidence block printed by the
// command line entry point. Values are derived from the same Report so the
// terminal output and the persisted artifact cannot drift apart.
func (r *Report) Summary() string {
	var b bytes.Buffer
	b.WriteString("evidence:\n")
	if len(r.Frames) > 0 {
		f := r.Frames[0]
		last := r.Frames[len(r.Frames)-1]
		b.WriteString("  frames: count=" + itoa(len(r.Frames)) +
			" first_sha256=" + f.Sha256 +
			" last_sha256=" + last.Sha256 +
			" first_pixel_mean=" + ftoa(f.Mean) + "\n")
	}
	if len(r.Audio) > 0 {
		var peak *AudioStats
		for i := range r.Audio {
			if peak == nil || r.Audio[i].Peak > peak.Peak {
				peak = &r.Audio[i]
			}
		}
		b.WriteString("  audio: blocks=" + itoa(len(r.Audio)) +
			" peak_amp=" + itoa(int(peak.Peak)) +
			" peak_dbfs=" + ftoa(peak.PeakDBFS) +
			" rms_dbfs=" + ftoa(peak.RMS) + "\n")
	}
	b.WriteString("  alignment: max_abs_error_tick=" + itoa(int(r.MaxAlignError)) +
		" missing=" + itoa(r.MissingEvents) + "\n")
	return b.String()
}
