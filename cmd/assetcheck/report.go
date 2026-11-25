package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"assetcheck/internal/cache"
	"assetcheck/internal/errmodel"
	"assetcheck/internal/mediaclock"
	"assetcheck/internal/stream"
	"assetcheck/internal/txn"
)

// FrameInfo is the audio geometry block of the report.
type FrameInfo struct {
	TotalFrames     int64   `json:"total-frames"`
	PresentFrames   int64   `json:"present-frames"`
	Channels        int     `json:"channels"`
	SampleRate      int     `json:"sample-rate"`
	BitsPerSample   int     `json:"bits-per-sample"`
	BlockAlign      int     `json:"block-align"`
	DurationSeconds float64 `json:"duration-seconds"`
}

// TxnView carries WAL sequence information.
type TxnView struct {
	TxnID         string `json:"txn-id,omitempty"`
	LastCommitSeq uint64 `json:"last-commit-seq"`
	CommitSeq     uint64 `json:"commit-seq"`
	PreparedSeq   uint64 `json:"prepared-seq,omitempty"`
	InjectedPoint string `json:"injected-failure,omitempty"`
}

// IndexView summarizes the visible index.
type IndexView struct {
	Dir        string `json:"dir"`
	ContentKey string `json:"content-key"`
	Digest     string `json:"digest,omitempty"`
	VisibleSeq uint64 `json:"visible-seq,omitempty"`
	Keys       int    `json:"keys"`
}

// ReportView is the complete deterministic terminal/JSON report.
type ReportView struct {
	File           string               `json:"file"`
	Mutation       string               `json:"mutation,omitempty"`
	SummaryDigest  string               `json:"summary-digest"`
	Fingerprints   cache.Fingerprints   `json:"fingerprints"`
	Audio          FrameInfo            `json:"audio"`
	Interval       stream.IntervalStats `json:"interval"`
	CorruptRanges  []mediaclock.Range   `json:"corrupt-ranges"`
	CorruptFrames  int64                `json:"corrupt-frames"`
	CorruptRatio   float64              `json:"corrupt-ratio"`
	BudgetExceeded bool                 `json:"budget-exceeded,omitempty"`
	LastFrame      int64                `json:"last-frame,omitempty"`
	Findings       []*errmodel.Error    `json:"findings,omitempty"`
	Items          []stream.Item        `json:"threshold-items"`
	EvalPass       bool                 `json:"eval-pass"`
	Invalidations  []cache.Invalidation `json:"invalidations,omitempty"`
	Recovery       *txn.RecoveryResult  `json:"recovery,omitempty"`
	Txn            TxnView              `json:"txn"`
	Index          IndexView            `json:"index"`
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// RenderText produces the greppable acceptance text report.
func RenderText(v *ReportView) string {
	var b strings.Builder
	fmt.Fprintln(&b, "=== assetcheck report ===")
	fmt.Fprintf(&b, "file: %s\n", v.File)
	if v.Mutation != "" {
		fmt.Fprintf(&b, "mutation-demonstration: %s (baseline cached -> mutated -> revalidated)\n", v.Mutation)
	}
	fmt.Fprintf(&b, "summary-digest: %s\n", v.SummaryDigest)

	fmt.Fprintln(&b, "fingerprints:")
	fmt.Fprintf(&b, "  content-fingerprint:   %s\n", v.Fingerprints.Content)
	fmt.Fprintf(&b, "  structure-fingerprint: %s\n", v.Fingerprints.Structure)
	fmt.Fprintf(&b, "  metadata-fingerprint:  %s\n", v.Fingerprints.Metadata)

	fmt.Fprintln(&b, "audio:")
	fmt.Fprintf(&b, "  frames=%d present-frames=%d channels=%d layout=%s sample-rate=%d bits=%d block-align=%d duration=%.3fs\n",
		v.Audio.TotalFrames, v.Audio.PresentFrames, v.Audio.Channels, stream.LayoutLabel(v.Audio.Channels),
		v.Audio.SampleRate, v.Audio.BitsPerSample, v.Audio.BlockAlign, v.Audio.DurationSeconds)

	fmt.Fprintln(&b, "intervals:")
	fmt.Fprintf(&b, "  boundaries=%d intervals=%d min-interval-frames=%d max-interval-frames=%d avg-interval-frames=%d\n",
		v.Interval.BoundaryCount, v.Interval.IntervalCount, v.Interval.MinFrames,
		v.Interval.MaxFrames, v.Interval.AvgFrames)

	fmt.Fprintln(&b, "corrupt-ranges:")
	if len(v.CorruptRanges) == 0 {
		fmt.Fprintln(&b, "  none")
	}
	for _, r := range v.CorruptRanges {
		fmt.Fprintf(&b, "  - start-frame=%d end-frame=%d frames=%d duration-ms=%d\n",
			r.Start, r.End, r.Frames(),
			v.Audio.durationMS(r.Frames()))
	}
	fmt.Fprintf(&b, "  corrupt-frames=%d corrupt-ratio=%.6f\n", v.CorruptFrames, v.CorruptRatio)
	if v.BudgetExceeded {
		fmt.Fprintf(&b, "  scan-budget-exceeded: last-processed-frame=%d\n", v.LastFrame)
	}

	fmt.Fprintln(&b, "errors:")
	if len(v.Findings) == 0 {
		fmt.Fprintln(&b, "  none")
	}
	for _, e := range v.Findings {
		fmt.Fprintf(&b, "  %s\n", e.Error())
	}

	fmt.Fprintln(&b, "thresholds:")
	for _, it := range v.Items {
		status := "PASS"
		if !it.Pass {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "  [%s] %s actual=%g threshold=%g comparator=%s deviation=%+g unit=%s (%s)\n",
			status, it.Key, it.Actual, it.Threshold, it.Comparator, it.Deviation, it.Unit, it.ActualText)
	}

	fmt.Fprintln(&b, "cache-invalidation:")
	if len(v.Invalidations) == 0 {
		fmt.Fprintln(&b, "  no invalidation: content/structure/metadata fingerprints match cache")
	}
	for _, inv := range v.Invalidations {
		fmt.Fprintf(&b, "  %s: INVALIDATED reason=%s\n", inv.Object, inv.Reason)
		if inv.Old != "" && inv.New != "" {
			fmt.Fprintf(&b, "    old=%s\n    new=%s\n", shortFP(inv.Old), shortFP(inv.New))
		}
	}

	if v.Recovery != nil {
		fmt.Fprintln(&b, "recovery:")
		fmt.Fprintf(&b, "  last-commit-seq=%d rolled-back=%v rolled-forward=%v removed-staging=%d\n",
			v.Recovery.LastCommitSeq, v.Recovery.RolledBack, v.Recovery.RolledForward, v.Recovery.RemovedStaging)
	}

	fmt.Fprintln(&b, "index:")
	fmt.Fprintf(&b, "  dir=%s content-key=%s keys=%d visible-seq=%d digest=%s\n",
		v.Index.Dir, v.Index.ContentKey, v.Index.Keys, v.Index.VisibleSeq, v.Index.Digest)

	fmt.Fprintln(&b, "wal:")
	if v.Txn.InjectedPoint != "" {
		fmt.Fprintf(&b, "  txn=%s last-commit-seq=%d prepared-seq=%d injected-failure=%s committed=no\n",
			v.Txn.TxnID, v.Txn.LastCommitSeq, v.Txn.PreparedSeq, v.Txn.InjectedPoint)
	} else {
		fmt.Fprintf(&b, "  txn=%s last-commit-seq=%d commit-seq=%d committed=yes\n",
			v.Txn.TxnID, v.Txn.LastCommitSeq, v.Txn.CommitSeq)
	}

	fmt.Fprintln(&b, "verdict:")
	if v.EvalPass {
		fmt.Fprintln(&b, "  PASS (all thresholds satisfied)")
	} else {
		fmt.Fprintln(&b, "  FAIL (one or more business thresholds exceeded)")
	}
	return b.String()
}

func (f FrameInfo) durationMS(frames int64) int64 {
	if f.SampleRate <= 0 {
		return 0
	}
	return frames * 1000 / int64(f.SampleRate)
}

func shortFP(fp string) string {
	if len(fp) > 24 {
		return fp[:24] + "..."
	}
	return fp
}
