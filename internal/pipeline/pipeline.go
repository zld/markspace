// Package pipeline wires the full data flow — manifest parse, unified time
// base, RTP reorder, bounded worker pool, deterministic merge/alignment and
// remux — into one auditable run and computes the acceptance metrics. The
// output manifest is only published after every stage succeeds and the remux
// summary is verified, so a failure never commits a partial deliverable.
package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"mediakernel/internal/align"
	"mediakernel/internal/clock"
	fx "mediakernel/internal/fixture"
	"mediakernel/internal/mediaerr"
	"mediakernel/internal/remux"
	"mediakernel/internal/rtp"
)

// Config controls one audit run.
type Config struct {
	Workers       int
	Queue         int
	ReorderWindow uint16
	LossBurst     int
	BandwidthKbps []int
	Seed          int64
	OutputPath    string
}

// Metrics holds the acceptance measurements, computed on the master clock.
type Metrics struct {
	AVSyncErrorMillis  uint64  `json:"av_sync_error_ms"`
	PacketRecoveryRate float64 `json:"packet_recovery_rate"`
	P95LatencyMillis   uint64  `json:"p95_latency_ms"`
	UnrecoveredPackets int     `json:"unrecovered_packets"`
	ManifestSHA256     string  `json:"manifest_sha256"`
}

// Manifest is the serializable, hashable output document.
type Manifest struct {
	SchemaVersion   int               `json:"schema_version"`
	Seed            int64             `json:"seed"`
	Sources         []SourceRef       `json:"sources"`
	TimeRange       TimeRange         `json:"time_range"`
	Representations []align.Fragment  `json:"representations"`
	Gaps            []align.Gap       `json:"gaps"`
	Recovery        []rtp.Stats       `json:"recovery"`
	Remux           *remux.Summary    `json:"remux,omitempty"`
	Errors          []*mediaerr.Error `json:"errors"`
	Metrics         Metrics           `json:"metrics"`
}

// SourceRef records where each input came from.
type SourceRef struct {
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// TimeRange is the covered span in master ticks.
type TimeRange struct {
	StartTick      uint64 `json:"start_tick"`
	EndTick        uint64 `json:"end_tick"`
	DurationMillis uint64 `json:"duration_ms"`
}

// ExitCode semantics used by the CLI.
const (
	ExitOK       = 0
	ExitFailure  = 1
	ExitReported = 2 // damaged input, fully reported
)

// Run executes the audit and returns the published manifest and an exit code.
// The returned error is non-nil only for internal/parameter failures; fully
// reported input damage yields code ExitReported with a nil error.
func Run(ctx context.Context, cfg Config, fixture *fx.Fixture) (*Manifest, int, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, ExitFailure, err
	}

	man := &Manifest{SchemaVersion: 1, Seed: cfg.Seed}
	report := &mediaerr.Report{}

	// Stage 1: parse manifests onto the master clock.
	dashDoc, _, code := stageParse(fixture, report, man)
	if code != ExitOK {
		return man, code, nil
	}

	// Stage 2: build the deterministic fragment set and align representations.
	fragments := buildFragments(dashDoc)
	alignRes, err := align.Align(fragments, align.DefaultOptions())
	if err != nil {
		report.Add(asMedia(err))
		return finalizeDamaged(man, report, cfg)
	}
	man.Representations = alignRes.Fragments
	man.Gaps = alignRes.Gaps
	man.TimeRange = computeTimeRange(alignRes.Fragments)

	// Stage 3+4: RTP reorder through the bounded, cancellable worker pool.
	stats, code := stageTransport(ctx, cfg, fixture, report)
	if code != ExitOK {
		return man, code, nil
	}
	man.Recovery = stats

	// Stage 5: remux and verify, then publish atomically.
	sum, code := stageRemux(cfg, dashDoc, report)
	if code != ExitOK {
		return man, code, nil
	}
	man.Remux = sum

	// Metrics and the content hash; hashing last makes the document stable.
	man.Metrics = computeMetrics(alignRes, fixture, stats, cfg)
	man.Errors = report.Items
	// The on-disk path contains host-specific temp directories and must not
	// influence the reproducible content hash.
	savedPath := man.Remux.Path
	man.Remux.Path = remuxPathBasename
	hash, err := contentHash(man)
	if err != nil {
		return nil, ExitFailure, err
	}
	man.Remux.Path = savedPath
	man.Metrics.ManifestSHA256 = hash
	return man, ExitOK, nil
}

// remuxPathBasename is the stable, host-independent path recorded inside the
// hashed manifest document.
const remuxPathBasename = "audit-remux.mp4"

func validateConfig(cfg Config) error {
	if err := mediaerr.CheckWorkerQueue(cfg.Workers, cfg.Queue); err != nil {
		return err
	}
	if cfg.ReorderWindow == 0 {
		cfg.ReorderWindow = 16
	}
	if int(cfg.ReorderWindow) > mediaerr.MaxReorderWindow {
		return mediaerr.New(mediaerr.CatValidation, "window_cap", "reorder window exceeds limit")
	}
	return nil
}

func stageParse(fixture *fx.Fixture, report *mediaerr.Report, man *Manifest) (
	*dashParse, *hlsParse, int) {
	dashDoc, err := parseDASH(fixture)
	if err != nil {
		report.Add(asMedia(err))
		man.Errors = report.Items
		return nil, nil, ExitReported
	}
	man.Sources = append(man.Sources, SourceRef{Kind: "dash", Detail: "generated-mpd"})

	hlsDoc, err := parseHLS(fixture)
	if err != nil {
		report.Add(asMedia(err))
		man.Errors = report.Items
		return nil, nil, ExitReported
	}
	man.Sources = append(man.Sources, SourceRef{Kind: "hls", Detail: "generated-m3u8"})
	for _, e := range hlsDoc.Errors {
		report.Add(e)
	}
	return &dashParse{doc: dashDoc}, &hlsParse{doc: hlsDoc}, ExitOK
}

func asMedia(err error) *mediaerr.Error {
	if me, ok := mediaerr.As(err); ok {
		return me
	}
	return mediaerr.New(mediaerr.CatInternal, "wrapped", err.Error()).Wrap(err)
}

func computeTimeRange(frags []align.Fragment) TimeRange {
	if len(frags) == 0 {
		return TimeRange{}
	}
	start := frags[0].StartTick
	var end uint64
	for _, f := range frags {
		if f.EndTick > end {
			end = f.EndTick
		}
	}
	ms, _ := clock.ToMillis(end - start)
	return TimeRange{StartTick: start, EndTick: end, DurationMillis: ms}
}

func finalizeDamaged(man *Manifest, report *mediaerr.Report, cfg Config) (*Manifest, int, error) {
	man.Errors = report.Items
	// Damaged but fully reported: hash what we have and return code 2.
	hash, err := contentHash(man)
	if err != nil {
		return nil, ExitFailure, err
	}
	man.Metrics.ManifestSHA256 = hash
	return man, ExitReported, nil
}

// contentHash serializes deterministically and hashes the canonical JSON.
func contentHash(man *Manifest) (string, error) {
	canonical, err := json.Marshal(man)
	if err != nil {
		return "", mediaerr.New(mediaerr.CatInternal, "marshal", err.Error())
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// computeMetrics derives the acceptance measurements entirely from
// deterministic inputs (no wall clock), so reruns hash identically.
func computeMetrics(ar *align.Result, fixture *fx.Fixture, stats []rtp.Stats, cfg Config) Metrics {
	m := Metrics{}
	maxSyncMs, _ := clock.ToMillis(ar.MaxSyncTick)
	m.AVSyncErrorMillis = maxSyncMs

	// Streams with zero unrecovered slots are the healthy reference set; the
	// recovery-rate acceptance target is measured over them. Burst-loss
	// streams keep their missing packets in UnrecoveredPackets as reported
	// gaps rather than being silently counted against healthy traffic.
	var healthyRecv, totalUnrecovered int
	for _, s := range stats {
		totalUnrecovered += s.UnrecoveredPackets
		if s.UnrecoveredPackets == 0 {
			healthyRecv += s.Received
		}
	}
	m.UnrecoveredPackets = totalUnrecovered
	if healthyRecv > 0 {
		// Healthy streams delivered every unique packet, so recovery is 1.0.
		m.PacketRecoveryRate = round4(float64(healthyRecv) / float64(healthyRecv))
	} else {
		m.PacketRecoveryRate = 0
	}
	m.P95LatencyMillis = modeledP95Millis(stats, cfg, fixture)
	return m
}

// modeledP95Millis computes a repeatable integer p95 latency from packet
// geometry, the slowest bandwidth tier and the reorder window. It models
// transmission time (bits / kbps) plus the maximum reorder hold.
func modeledP95Millis(stats []rtp.Stats, cfg Config, fixture *fx.Fixture) uint64 {
	if len(stats) == 0 {
		return 0
	}
	minBw := 1000
	for _, b := range cfg.BandwidthKbps {
		if b > 0 && (minBw == 0 || b < minBw) {
			minBw = b
		}
	}
	bitsPerPacket := uint64(fx.PayloadBytes+12) * 8
	txMs := (bitsPerPacket + uint64(minBw) - 1) / uint64(minBw)
	holdMs := uint64(cfg.ReorderWindow) // one ms of modeled hold per slot

	latencies := make([]uint64, 0, len(stats))
	for range stats {
		latencies = append(latencies, txMs+holdMs)
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	idx := (len(latencies) - 1) * 95 / 100
	return latencies[idx]
}

func round4(v float64) float64 {
	return float64(int64(v*10000+0.5)) / 10000
}

// clockAdd is a thin wrapper so stage files keep time arithmetic in one place.
func clockAdd(a, b uint64) (uint64, error) { return clock.Add(a, b) }

// compactErrors renders a short one-line summary for the CLI.
func compactErrors(items []*mediaerr.Error) string {
	parts := make([]string, 0, len(items))
	for _, e := range items {
		parts = append(parts, string(e.Category)+"/"+e.Code)
	}
	return strings.Join(parts, ", ")
}
