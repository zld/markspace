// Package policy chooses a delivery rendition for each segment under three
// constraints, applied in this order: the quality floor is inviolable, the
// processing budget is capped next, and the byte ceiling is satisfied last by
// compressing within the remaining quality range.
package policy

import (
	"fmt"
	"sort"

	"replaycheck/internal/model"
	"replaycheck/internal/split"
)

// PPM expresses a quality in parts per million, so 0.90 is 900000. Integers
// keep budgets exactly comparable; no float ever decides a tradeoff.
type PPM int64

// Level is one rendition rung in the ladder.
type Level struct {
	Name       string `json:"name"`
	QualityPPM PPM    `json:"qualityPpm"`
	BytePPM    PPM    `json:"bytePpm"`    // output bytes per million source bytes
	ProcMicros int64  `json:"procMicros"` // processing microseconds per KiB source
}

// DefaultLadder is ordered best-first. The lowest rung sits exactly at the
// acceptance quality floor so a squeezed delivery can still qualify.
func DefaultLadder() []Level {
	return []Level{
		{Name: "high", QualityPPM: 980_000, BytePPM: 1_000_000, ProcMicros: 2000},
		{Name: "balanced", QualityPPM: 950_000, BytePPM: 650_000, ProcMicros: 1200},
		{Name: "compact", QualityPPM: 920_000, BytePPM: 400_000, ProcMicros: 800},
		{Name: "economy", QualityPPM: 900_000, BytePPM: 270_000, ProcMicros: 500},
	}
}

// Budget is the delivery envelope requested by the operator.
type Budget struct {
	MaxBytes   int64 `json:"maxBytes"`
	MaxProcMS  int64 `json:"maxProcMs"`
	MinQuality PPM   `json:"minQualityPpm"`
}

// Choice is the selected rendition for one segment.
type Choice struct {
	Segment     int    `json:"segment"`
	Level       string `json:"level"`
	QualityPPM  PPM    `json:"qualityPpm"`
	SourceBytes int64  `json:"sourceBytes"`
	Bytes       int64  `json:"bytes"`
	ProcMS      int64  `json:"procMs"`
	Trigger     string `json:"trigger"`
	StartTick   int64  `json:"startTick"`
	EndTick     int64  `json:"endTick"`
}

// Result is a feasible delivery plan.
type Result struct {
	Choices       []Choice `json:"choices"`
	TotalBytes    int64    `json:"totalBytes"`
	TotalProcMS   int64    `json:"totalProcMs"`
	MinQualityPPM PPM      `json:"minQualityPpm"`
	PolicyDigest  string   `json:"policyDigest"`
}

// Conflict describes an envelope that cannot be met. It reports the field that
// failed, what was asked, and the best value actually achievable, together with
// the tradeoffs that were made before giving up. No manifest is produced.
type Conflict struct {
	Field      string   `json:"field"`
	Requested  int64    `json:"requested"`
	Achievable int64    `json:"achievable"`
	Detail     string   `json:"detail"`
	Tradeoffs  []Choice `json:"tradeoffs"`
}

func (c *Conflict) Error() string {
	return fmt.Sprintf("budget conflict: %s requested=%d achievable=%d: %s", c.Field, c.Requested, c.Achievable, c.Detail)
}

func (b Budget) Validate() error {
	if b.MaxBytes <= 0 {
		return model.NewError(model.StageDerive, "budget", model.TrackRef{}, 0, "byte budget must be positive")
	}
	if b.MaxProcMS <= 0 {
		return model.NewError(model.StageDerive, "budget", model.TrackRef{}, 0, "processing budget must be positive")
	}
	if b.MinQuality < 0 || b.MinQuality > 1_000_000 {
		return model.NewError(model.StageDerive, "budget", model.TrackRef{}, 0, "quality floor must be within [0,1]")
	}
	return nil
}

// Digest identifies the policy so cache entries cannot be reused across a
// changed ladder or envelope.
func (b Budget) Digest(ladder []Level) (string, error) {
	return model.DigestJSON(struct {
		Budget Budget  `json:"budget"`
		Ladder []Level `json:"ladder"`
	}{b, ladder})
}

func procMS(sourceBytes int64, l Level) int64 {
	us := sourceBytes * l.ProcMicros / 1024
	if us%1000 != 0 {
		us += 1000
	}
	return us / 1000
}

func outBytes(sourceBytes int64, l Level) int64 {
	return sourceBytes * int64(l.BytePPM) / 1_000_000
}

// Decide selects one level per segment. It starts from the best rung that
// clears the quality floor and steps segments down only as far as needed. It
// returns a nil Result and a Conflict when the envelope is impossible, so the
// caller cannot accidentally publish an unqualified manifest.
func Decide(segments []split.Segment, b Budget, ladder []Level) (*Result, *Conflict, error) {
	if err := b.Validate(); err != nil {
		return nil, nil, err
	}
	if len(segments) == 0 {
		return nil, nil, model.NewError(model.StageDerive, "budget", model.TrackRef{}, 0, "no segments to deliver")
	}
	if len(ladder) == 0 {
		return nil, nil, model.NewError(model.StageDerive, "budget", model.TrackRef{}, 0, "empty rendition ladder")
	}
	// Sorted best-first; the floor cuts off rungs that may never be used.
	sorted := append([]Level(nil), ladder...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].QualityPPM > sorted[j].QualityPPM })
	eligible := make([]Level, 0, len(sorted))
	for _, l := range sorted {
		if l.QualityPPM >= b.MinQuality {
			eligible = append(eligible, l)
		}
	}
	if len(eligible) == 0 {
		return nil, &Conflict{
			Field: "quality", Requested: int64(b.MinQuality), Achievable: int64(sorted[0].QualityPPM),
			Detail: "no rendition meets the quality floor",
		}, nil
	}

	rank := make([]int, len(segments)) // index into eligible, 0 = best
	build := func() *Result {
		res := &Result{MinQualityPPM: eligible[0].QualityPPM}
		for i, seg := range segments {
			l := eligible[rank[i]]
			c := Choice{
				Segment: i, Level: l.Name, QualityPPM: l.QualityPPM,
				SourceBytes: int64(seg.Bytes), Bytes: outBytes(int64(seg.Bytes), l),
				ProcMS: procMS(int64(seg.Bytes), l), Trigger: seg.StartName,
				StartTick: int64(seg.Span.Start), EndTick: int64(seg.Span.End),
			}
			res.Choices = append(res.Choices, c)
			res.TotalBytes += c.Bytes
			res.TotalProcMS += c.ProcMS
			if l.QualityPPM < res.MinQualityPPM {
				res.MinQualityPPM = l.QualityPPM
			}
		}
		return res
	}
	stepDown := func(pred func(*Result) bool, metric func(Choice) int64) (*Result, bool) {
		cur := build()
		for pred(cur) {
			bestSeg, bestSaving := -1, int64(0)
			for i := range segments {
				if rank[i]+1 >= len(eligible) {
					continue
				}
				now := metric(cur.Choices[i])
				next := metric(candidate(segments[i], eligible[rank[i]+1]))
				if saving := now - next; saving > bestSaving {
					bestSaving, bestSeg = saving, i
				}
			}
			if bestSeg < 0 {
				return cur, false
			}
			rank[bestSeg]++
			cur = build()
		}
		return cur, true
	}

	// Processing budget: second in precedence.
	res, ok := stepDown(func(r *Result) bool { return r.TotalProcMS > b.MaxProcMS }, func(c Choice) int64 { return c.ProcMS })
	if !ok {
		return nil, &Conflict{
			Field: "processingMs", Requested: b.MaxProcMS, Achievable: res.TotalProcMS,
			Detail:    "quality floor reached before the processing budget was met",
			Tradeoffs: res.Choices,
		}, nil
	}
	// Byte ceiling: last, and only within the quality floor.
	res, ok = stepDown(func(r *Result) bool { return r.TotalBytes > b.MaxBytes }, func(c Choice) int64 { return c.Bytes })
	if !ok {
		return nil, &Conflict{
			Field: "bytes", Requested: b.MaxBytes, Achievable: res.TotalBytes,
			Detail:    "quality floor reached before the byte ceiling was met",
			Tradeoffs: res.Choices,
		}, nil
	}
	if res.MinQualityPPM < b.MinQuality {
		return nil, &Conflict{
			Field: "quality", Requested: int64(b.MinQuality), Achievable: int64(res.MinQualityPPM),
			Detail: "internal: selection fell below the quality floor", Tradeoffs: res.Choices,
		}, nil
	}
	digest, err := b.Digest(ladder)
	if err != nil {
		return nil, nil, model.WrapError(model.StageDerive, "budget", model.TrackRef{}, 0, "cannot digest policy", err)
	}
	res.PolicyDigest = digest
	return res, nil, nil
}

func candidate(seg split.Segment, l Level) Choice {
	return Choice{
		SourceBytes: int64(seg.Bytes),
		Bytes:       outBytes(int64(seg.Bytes), l),
		ProcMS:      procMS(int64(seg.Bytes), l),
	}
}
