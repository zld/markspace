package engine

import (
	"github.com/example/tsrepair/internal/index"
	"github.com/example/tsrepair/internal/model"
	"github.com/example/tsrepair/internal/pipeline"
	"github.com/example/tsrepair/internal/segment"
	"github.com/example/tsrepair/internal/ts"
)

func (e *Engine) finalize(stats *pipeline.Stats, tr *ts.TruncatedRange, runErr error) (*Metrics, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ix := e.builder.Build()
	summaries := e.hasher.Summaries()
	segment.AttachIndexRanges(summaries, ix)
	hashOK, hashTotal := e.verifyHashes(summaries)
	hits, seeks := e.verifyKeyframes(ix)
	ccStats := e.cc.Stats()
	m := &Metrics{
		PacketsAccepted:    stats.Processed,
		RepairedWraps:      e.tsCorr.WrapCount(),
		IgnoredDuplicates:  len(ccStats.Duplicates),
		ContinuityGaps:     len(ccStats.Gaps),
		PSIVersions:        len(e.psi.Boundaries()),
		AudioErrors:        e.audio.ErrorCount(),
		TruncatedBytes:     truncatedLen(tr),
		MaxQueueDepth:      stats.MaxQueueDepth,
		SegmentCount:       len(summaries),
		RecoveredDrops:     stats.Recovered,
		HashMatchPct:       pct(hashOK, hashTotal),
		KeyframeHitRatePct: pct(hits, seeks),
		ParseRoundtripPct:  e.roundtripPct(stats),
	}
	m.PacketsIn = e.stream.TotalPackets
	if se := model.AsStreamError(runErr); se != nil {
		// Non-recover mode surfaces every classified stage fault; recover
		// mode leaves runErr nil because the runner already handled it.
		m.Fatal = se
		return m, se
	}
	return m, nil
}

// verifyHashes recomputes every fully-confirmed segment digest from the
// stored raw packets. Segments containing recovered (dropped) packets cannot
// be re-verified byte-for-byte and are excluded rather than counted as misses.
func (e *Engine) verifyHashes(summaries []segment.Summary) (ok, total int) {
	for _, s := range summaries {
		var buf []byte
		complete := true
		// Re-hash exactly the ordinals the segment was built from; the
		// summary makes the membership auditable for independent nodes.
		for _, ord := range s.Ordinals {
			raw, exists := e.rawByOrd[ord]
			if !exists {
				complete = false
				break
			}
			buf = append(buf, raw...)
		}
		if !complete {
			continue
		}
		total++
		if s.Recompute(buf) {
			ok++
		}
	}
	return ok, total
}

// verifyKeyframes reads every indexed frame back from the complete stream.
func (e *Engine) verifyKeyframes(ix *index.Index) (hits, seeks int) {
	for _, f := range ix.Frames {
		seeks++
		if err := ix.VerifySeek(f, e.stream.CompleteData); err == nil {
			hits++
		}
	}
	return hits, seeks
}

func (e *Engine) roundtripPct(stats *pipeline.Stats) float64 {
	// Round-trip measures how much of the eligible (non-null) input survived
	// into confirmed work; null stuffing is never part of the media timeline.
	eligible := e.eligible
	if eligible == 0 {
		return 100
	}
	return pct(stats.Processed, eligible)
}

func truncatedLen(tr *ts.TruncatedRange) int {
	if tr == nil {
		return 0
	}
	return tr.Length
}

func pct(part, total int) float64 {
	if total == 0 {
		return 100
	}
	return float64(part) * 100 / float64(total)
}
