// Package pipeline wires the scan, timeline, cache, recover, manifest and
// verify subsystems into one end-to-end, range-scoped run and reports the
// acceptance metrics each scenario must produce.
package pipeline

import (
	"context"
	"fmt"
	"os"

	"mediad/internal/cache"
	"mediad/internal/fixture"
	"mediad/internal/manifest"
	"mediad/internal/model"
	"mediad/internal/recover"
	"mediad/internal/source"
	"mediad/internal/timeline"
	"mediad/internal/verify"
)

// Row is the per-scenario acceptance record printed by the demo command.
type Row struct {
	Scenario           string  `json:"scenario"`
	TargetKbps         int     `json:"target_kbps"`
	ActualKbps         int     `json:"actual_kbps"`
	BitrateDeltaPct    float64 `json:"bitrate_delta_pct"`
	PSNRDB             float64 `json:"psnr_db"`
	ManifestMaxErrorMs float64 `json:"manifest_max_error_ms"`
	AnomalyCount       int     `json:"anomaly_count"`
	RecoveredSegments  int     `json:"recovered_segments"`
	CacheHit           bool    `json:"cache_hit"`
	IndexRebuilt       bool    `json:"index_rebuilt"`
	ExitCode           int     `json:"exit_code"`
	ManifestHash       string  `json:"manifest_hash"`
}

// Dirs groups the persistent on-disk locations shared across runs.
type Dirs struct {
	Work     string // scenario working copies and generated fixtures
	Store    string // immutable source CAS
	Cache    string // derived segment cache and index store
	Manifest string
}

// extractProcessor copies segment bytes as the "derived" output. It is the
// bounded derivation stand-in: real transcoding would decode only the segment.
type extractProcessor struct {
	ra        *os.File
	corruptAt map[int64]bool // source offsets that are unusable
}

func (p *extractProcessor) Process(seg recover.Segment) ([]byte, error) {
	if p.corruptAt[seg.Offset] {
		return nil, recover.ErrSourceCorrupt
	}
	buf := make([]byte, seg.Length)
	if _, err := p.ra.ReadAt(buf, seg.Offset); err != nil {
		return nil, fmt.Errorf("pipeline: read segment: %w", err)
	}
	return buf, nil
}

// Run executes one scenario over [fromNanos,toNanos] and returns its metrics.
// It is deterministic: identical inputs and caches yield identical rows and
// manifest hashes.
func Run(d Dirs, b *fixture.Bundle, mp4Path, scenario string,
	fromNanos, toNanos int64, params cache.Params, runner verify.Runner,
	forceCacheCorruption bool) (*Row, error) {

	// 1. Ingest the source into the immutable CAS (replacement-safe).
	srcStore, err := source.NewStore(d.Store)
	if err != nil {
		return nil, err
	}
	asset, err := srcStore.Ingest(context.Background(), mp4Path)
	if err != nil {
		return nil, err
	}

	// 2. Decide whether the index must rebuild for this source hash.
	cacheStore, err := cache.New(d.Cache)
	if err != nil {
		return nil, err
	}
	cachedIndex, _ := cacheStore.LoadIndex(asset.Hash)

	// 3. Scan the immutable blob: MP4 tracks/samples/emsg plus sidecars.
	res, err := scanAll(asset, b)
	if err != nil {
		return nil, err
	}

	// 4. Normalize onto the canonical nanosecond time base.
	norm, err := timeline.Normalize(res)
	if err != nil {
		return nil, err
	}
	index, err := timeline.BuildIndex(norm)
	if err != nil {
		return nil, err
	}
	indexRebuilt := timeline.NeedsRebuild(cachedIndex, index)
	if indexRebuilt {
		if err := cacheStore.SaveIndex(index); err != nil {
			return nil, err
		}
	}

	// 5. Build sync-bounded segments for the primary video track.
	trackID := primaryVideoTrack(norm)
	blob, err := os.Open(asset.BlobPath)
	if err != nil {
		return nil, err
	}
	defer blob.Close()
	set, err := recover.BuildSegments(norm, trackID, params, blob)
	if err != nil {
		return nil, err
	}

	// 6. Fault injection for the damage scenario: corrupt a derived cache
	// entry for a segment that lies inside the requested time window, so the
	// resulting checksum anomaly appears in the windowed manifest with its
	// source offset, and exactly that segment recomputes from its sync boundary.
	knownFaults := map[int]model.Anomaly{}
	if forceCacheCorruption && len(set.Segments) > 0 {
		victim := segmentInWindow(set.Segments, fromNanos, toNanos)
		_ = cacheStore.Put(victim.CacheKey, []byte("stale-corrupt-bytes"))
		anom := model.Anomaly{
			Type:    model.AnomalyChecksum,
			Message: "derived segment checksum failed",
		}
		if tr := findTrack(norm, trackID); tr != nil {
			anom.TrackKind = tr.Kind
			anom.TimeBase = tr.TimeBase
			if raw, err := tr.TimeBase.ToTicks(victim.StartNanos); err == nil {
				anom.RawTick = raw
			}
		}
		knownFaults[victim.ID] = anom
	}

	// 7. Cache-first, range-scoped recovery.
	proc := &extractProcessor{ra: blob}
	rec := recover.New(cacheStore, proc)
	recResult, err := rec.Run(set, knownFaults)
	if err != nil {
		return nil, err
	}

	// 8. Windowed manifest and quantitative metrics.
	allAnoms := append(append([]model.Anomaly{}, norm.Anomalies...), recResult.Anomalies...)
	man := manifest.Window(asset.Hash, fromNanos, toNanos, allAnoms)
	_, manHash, err := manifest.Write(d.Manifest, man)
	if err != nil {
		return nil, err
	}

	row, err := buildMetrics(b, mp4Path, runner, norm, man, recResult, indexRebuilt, manHash, scenario)
	if err != nil {
		return nil, err
	}
	row.ExitCode = model.ExitOK
	return row, nil
}
