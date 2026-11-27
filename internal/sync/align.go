// Package sync aligns dialogue audio and subtitle tracks on the shared
// nanosecond timeline and quantifies A/V synchronization error.
package sync

import (
	"sort"

	"mediasync/internal/index"
	"mediasync/internal/model"
)

// Cue is one subtitle interval on the canonical timeline.
type Cue struct {
	ID      string
	StartNS model.Ticks
	EndNS   model.Ticks
	Text    string
}

// CueResult reports the alignment decision for one cue.
type CueResult struct {
	Cue        Cue         `json:"cue"`
	Matched    bool        `json:"matched"`
	SyncErrNS  model.Ticks `json:"sync_error_ns"`
	AbsErrNS   model.Ticks `json:"abs_error_ns"`
	AudioIndex int         `json:"audio_index"`
	Missing    bool        `json:"missing"`
}

// Report is the alignment summary.
type Report struct {
	Cues        []CueResult `json:"cues"`
	MaxAbsErrNS model.Ticks `json:"max_abs_error_ns"`
	P95ErrNS    model.Ticks `json:"p95_error_ns"`
	Matched     int         `json:"matched"`
	Missing     int         `json:"missing"`
	Errors      []model.MediaError `json:"errors,omitempty"`
}

// Align matches every cue against the audio sample whose interval contains
// (or is closest to) the cue start, using only integer-nanosecond math.
// A cue whose start falls outside every audio interval is marked missing
// rather than silently dropped.
func Align(audio *index.TrackIndex, cues []Cue) (*Report, error) {
	if audio == nil {
		return nil, model.NewError(model.ClassMissingTimestamp, "no audio track available for alignment")
	}
	rep := &Report{Cues: make([]CueResult, 0, len(cues))}
	if len(audio.Samples) == 0 {
		for _, c := range cues {
			rep.Cues = append(rep.Cues, CueResult{Cue: c, Missing: true, AudioIndex: -1})
			rep.Missing++
		}
		return rep, nil
	}
	absErrs := make([]model.Ticks, 0, len(cues))
	for _, cue := range cues {
		res := alignOne(audio, cue)
		rep.Cues = append(rep.Cues, res)
		if res.Missing {
			rep.Missing++
			continue
		}
		rep.Matched++
		absErrs = append(absErrs, res.AbsErrNS)
		if res.AbsErrNS > rep.MaxAbsErrNS {
			rep.MaxAbsErrNS = res.AbsErrNS
		}
	}
	rep.P95ErrNS = percentile(absErrs, 95)
	return rep, nil
}

// alignOne finds the audio sample overlapping the cue start. When no sample
// contains it, the nearest boundary decides; beyond the track extents the
// cue is missing.
func alignOne(audio *index.TrackIndex, cue Cue) CueResult {
	res := CueResult{Cue: cue, AudioIndex: -1}
	first := audio.Samples[0]
	last := audio.Samples[len(audio.Samples)-1]
	// A cue starting before the first sample or after the last interval end
	// has no corresponding media region.
	if cue.StartNS < first.StartNS || cue.StartNS > last.EndNS {
		res.Missing = true
		return res
	}
	// Binary search: first sample whose EndNS is strictly greater than cue start.
	idx := sort.Search(len(audio.Samples), func(i int) bool {
		return audio.Samples[i].EndNS > cue.StartNS
	})
	if idx >= len(audio.Samples) {
		res.Missing = true
		return res
	}
	s := audio.Samples[idx]
	res.AudioIndex = idx
	res.Matched = true
	// Signed error: positive means the subtitle leads the audio start.
	res.SyncErrNS = cue.StartNS - s.StartNS
	res.AbsErrNS = abs(res.SyncErrNS)
	return res
}

// percentile returns the nearest-rank P-pct value of a sorted copy of vals.
func percentile(vals []model.Ticks, pct int) model.Ticks {
	if len(vals) == 0 {
		return 0
	}
	sorted := append([]model.Ticks(nil), vals...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if pct <= 0 {
		return sorted[0]
	}
	if pct >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := (len(sorted)*pct + 99) / 100 // ceil(n*p/100), 1-based
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

func abs(t model.Ticks) model.Ticks {
	if t < 0 {
		return -t
	}
	return t
}

// BuildCuesFromTrack converts a subtitle track's samples into cues using the
// sample text payload length as content; useful for fully synthetic inputs.
func BuildCuesFromTrack(sub *index.TrackIndex) []Cue {
	if sub == nil {
		return nil
	}
	cues := make([]Cue, 0, len(sub.Samples))
	for i, s := range sub.Samples {
		cues = append(cues, Cue{
			ID:      cueID(sub.TrackID, i),
			StartNS: s.StartNS,
			EndNS:   s.EndNS,
			Text:    cueText(i),
		})
	}
	return cues
}

// BuildCuesWithJitter returns cues whose starts are shifted by jitter[i]
// nanoseconds so tests can inject measurable sync error.
func BuildCuesWithJitter(sub *index.TrackIndex, jitter []model.Ticks) []Cue {
	base := BuildCuesFromTrack(sub)
	for i := range base {
		if i < len(jitter) {
			base[i].StartNS += jitter[i]
		}
	}
	return base
}

func cueID(trackID uint32, i int) string {
	return "t" + itoa(trackID) + "-" + itoa(uint32(i))
}

func cueText(i int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	return string(alphabet[i%len(alphabet)])
}

func itoa(v uint32) string {
	if v == 0 {
		return "0"
	}
	var buf [12]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[pos:])
}
