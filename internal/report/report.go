// Package report renders verification results as terminal text or JSON.
// Both forms are built from one summary so their numbers cannot diverge.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"streamcheck/internal/model"
	"streamcheck/internal/verify"
)

// RateSummary is the stable JSON shape for one rate.
type RateSummary struct {
	Rate                int   `json:"rate"`
	SegmentCount        int   `json:"segment_count"`
	DurationMS          int64 `json:"duration_ms"`
	DurationDeviationMS int64 `json:"duration_deviation_ms"`
	MissingCount        int   `json:"missing_count"`
	OutOfOrderCount     int   `json:"out_of_order_count"`
	DuplicateCount      int   `json:"duplicate_count"`
	ChecksumFailures    int   `json:"checksum_failures"`
	HasManifest         bool  `json:"has_manifest"`
}

// Verification is the JSON document emitted by verify --json.
type Verification struct {
	Status              string        `json:"status"`
	Root                string        `json:"root"`
	RateCount           int           `json:"rate_count"`
	SegmentCount        int           `json:"segment_count"`
	DurationMS          int64         `json:"duration_ms"`
	DurationDeviationMS int64         `json:"duration_deviation_ms"`
	MissingCount        int           `json:"missing_count"`
	OutOfOrderCount     int           `json:"out_of_order_count"`
	DuplicateCount      int           `json:"duplicate_count"`
	ChecksumFailures    int           `json:"checksum_failures"`
	MissingTolerance    int           `json:"missing_tolerance"`
	Rates               []RateSummary `json:"rates"`
	Issues              []model.Issue `json:"issues"`
}

// Error is the JSON document emitted when verify cannot run at all.
type Error struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// NewVerification converts a verify.Result into the reported summary. The
// aggregate duration is the reference rate's total, and the aggregate
// deviation is the largest per-rate deviation.
func NewVerification(res verify.Result) Verification {
	v := Verification{
		Status:              string(model.StatusPass),
		Root:                res.Root,
		RateCount:           len(res.Rates),
		DurationDeviationMS: res.MaxDeviationMS,
		MissingCount:        res.MissingCount,
		OutOfOrderCount:     res.OutOfOrderCount,
		DuplicateCount:      res.DuplicateCount,
		ChecksumFailures:    res.ChecksumFailures,
		MissingTolerance:    res.MissingTolerance,
		Issues:              res.Issues,
	}
	rates := append([]verify.RateCheck(nil), res.Rates...)
	sort.Slice(rates, func(i, j int) bool { return rates[i].Rate < rates[j].Rate })
	longest := 0
	for _, rate := range rates {
		v.Rates = append(v.Rates, RateSummary{
			Rate:                rate.Rate,
			SegmentCount:        rate.SegmentCount,
			DurationMS:          rate.DurationMS,
			DurationDeviationMS: rate.DurationDeviationMS,
			MissingCount:        rate.MissingCount,
			OutOfOrderCount:     rate.OutOfOrderCount,
			DuplicateCount:      rate.DuplicateCount,
			ChecksumFailures:    rate.ChecksumFailures,
			HasManifest:         rate.HasManifest,
		})
		v.SegmentCount += rate.SegmentCount
		if rate.SegmentCount > longest {
			longest = rate.SegmentCount
			v.DurationMS = rate.DurationMS
		}
	}
	if v.Issues == nil {
		v.Issues = []model.Issue{}
	}
	if res.Failed() {
		v.Status = string(model.StatusFail)
	}
	return v
}

// Text writes a human-readable summary. Segment counts, durations and issue
// totals match the JSON document field for field.
func Text(w io.Writer, v Verification) error {
	fmt.Fprintf(w, "root=%s\n", v.Root)
	fmt.Fprintf(w, "status=%s rate_count=%d segment_count=%d duration_ms=%d\n",
		v.Status, v.RateCount, v.SegmentCount, v.DurationMS)
	fmt.Fprintf(w, "duration_deviation_ms=%d missing_count=%d missing_tolerance=%d\n",
		v.DurationDeviationMS, v.MissingCount, v.MissingTolerance)
	fmt.Fprintf(w, "out_of_order_count=%d duplicate_count=%d checksum_failures=%d\n",
		v.OutOfOrderCount, v.DuplicateCount, v.ChecksumFailures)

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "RATE\tSEGMENTS\tDURATION_MS\tDEVIATION_MS\tMISSING\tOUT_OF_ORDER\tDUPLICATE\tCHECKSUM_FAILURES")
	for _, rate := range v.Rates {
		fmt.Fprintf(tw, "%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n",
			rate.Rate, rate.SegmentCount, rate.DurationMS, rate.DurationDeviationMS,
			rate.MissingCount, rate.OutOfOrderCount, rate.DuplicateCount, rate.ChecksumFailures)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(v.Issues) == 0 {
		fmt.Fprintln(w, "issues=none")
		return nil
	}
	fmt.Fprintf(w, "issues=%d\n", len(v.Issues))
	for _, issue := range v.Issues {
		fmt.Fprintf(w, "  - %s\n", issue)
	}
	return nil
}

// WriteJSON writes the summary as indented JSON with a trailing newline.
func WriteJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
