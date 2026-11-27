package pipeline

import (
	"os"

	"medtrace/internal/mederr"
	"medtrace/internal/state"
	"medtrace/internal/track"
)

// Stage names recorded in checkpoints.
const (
	StageMedia   = "media_extract"
	StageGraph   = "track_graph"
	StageNetwork = "network_recovery"
	StageSync    = "av_sync"
)

// Params holds every tunable that participates in stage idempotency keys.
type Params struct {
	RecoveryWindowNs int64 `json:"recovery_window_ns"`
	SyncToleranceMs  int64 `json:"sync_tolerance_ms"`
}

// DefaultParams matches the fixture generator's recovery window.
func DefaultParams() Params {
	return Params{RecoveryWindowNs: 5_000_000, SyncToleranceMs: 20}
}

// Report is the final structured result printed by the CLI and persisted
// across stages so resume/report never recompute validated work.
type Report struct {
	AVSyncErrorMs    float64             `json:"av_sync_error_ms"`
	SyncPairs        int64               `json:"sync_pairs"`
	MissingVideo     int64               `json:"missing_video_samples"`
	MissingAudio     int64               `json:"missing_audio_samples"`
	LossRecoveryRate float64             `json:"loss_recovery_rate"`
	P95LatencyMs     float64             `json:"p95_latency_ms"`
	DuplicatePackets int64               `json:"duplicate_packets"`
	ReorderedPackets int64               `json:"reordered_packets"`
	LostPackets      int64               `json:"lost_packets"`
	RecoveredPackets int64               `json:"recovered_packets"`
	InputSHA256      map[string]string   `json:"input_sha256"`
	CompletedStages  []string            `json:"completed_stages"`
	Order            []uint64            `json:"track_order"`
	Offsets          []state.OffsetRange `json:"byte_offsets"`
}

// Runner executes the four stages against one checkpoint directory.
type Runner struct {
	stateDir string
	params   Params
}

// NewRunner constructs a runner.
func NewRunner(stateDir string, params Params) *Runner {
	return &Runner{stateDir: stateDir, params: params}
}

func (r *Runner) paramsMap() map[string]any {
	return map[string]any{
		"recovery_window_ns": r.params.RecoveryWindowNs,
		"sync_tolerance_ms":  r.params.SyncToleranceMs,
	}
}

// Run performs the full verify pipeline. mediaPath/eventsPath are hashed for
// provenance; allowRecovery governs corrupt-checkpoint behavior.
func (r *Runner) Run(mediaPath, eventsPath string, allowRecovery bool) (*Report, error) {
	run, err := state.Begin(r.stateDir, allowRecovery)
	if err != nil {
		return nil, err
	}
	mediaProv, err := state.SummarizeFile(mediaPath)
	if err != nil {
		return nil, err
	}
	eventsProv, err := state.SummarizeFile(eventsPath)
	if err != nil {
		return nil, err
	}
	run.SetInputs([]state.Provenance{mediaProv, eventsProv})

	report := &Report{InputSHA256: run.InputHashes()}
	pm := r.paramsMap()

	// Stage 1: media extraction (single-pass EBML walk).
	var extract *MediaExtract
	if rerun, _, err := run.ShouldRun(StageMedia, pm); err != nil {
		return nil, err
	} else if rerun {
		data, err := os.ReadFile(mediaPath)
		if err != nil {
			return nil, mederr.New(mederr.CatIO, "read media %q", mediaPath).Wrap(err)
		}
		extract, err = ExtractMedia(data)
		if err != nil {
			return nil, err
		}
		stats := map[string]any{"blocks": len(extract.Blocks), "tracks": len(extract.Tracks)}
		if err := run.Record(StageMedia, pm, blockOffsets(extract), stats); err != nil {
			return nil, err
		}
	}

	// Stage 2: track dependency graph (cycle is a hard input error).
	var order []uint64
	if rerun, _, err := run.ShouldRun(StageGraph, pm); err != nil {
		return nil, err
	} else if rerun {
		if extract == nil {
			extract, err = r.reextract(mediaPath)
			if err != nil {
				return nil, err
			}
		}
		order, err = buildOrder(extract.Tracks)
		if err != nil {
			return nil, err
		}
		if err := run.Record(StageGraph, pm, nil, map[string]any{"order": order}); err != nil {
			return nil, err
		}
	}

	// Stage 3: network recovery.
	var netStats []map[string]any
	if rerun, _, err := run.ShouldRun(StageNetwork, pm); err != nil {
		return nil, err
	} else if rerun {
		stats, err := AnalyzeEvents(eventsPath, r.params.RecoveryWindowNs)
		if err != nil {
			return nil, err
		}
		netStats = make([]map[string]any, 0, len(stats))
		for _, st := range stats {
			netStats = append(netStats, map[string]any{
				"ssrc": st.SSRC, "received": st.Received, "duplicates": st.Duplicates,
				"reordered": st.Reordered, "lost": st.Lost, "recovered": st.Recovered,
			})
		}
		if err := run.Record(StageNetwork, pm, nil, map[string]any{"streams": netStats}); err != nil {
			return nil, err
		}
	}

	// Stage 4: A/V sync on the shared nanosecond base.
	if rerun, _, err := run.ShouldRun(StageSync, pm); err != nil {
		return nil, err
	} else if rerun {
		if extract == nil {
			extract, err = r.reextract(mediaPath)
			if err != nil {
				return nil, err
			}
		}
		syncStats := computeSync(extract)
		if err := run.Record(StageSync, pm, blockOffsets(extract), syncStats); err != nil {
			return nil, err
		}
	}

	return r.assemble(run, report)
}

func (r *Runner) reextract(mediaPath string) (*MediaExtract, error) {
	data, err := os.ReadFile(mediaPath)
	if err != nil {
		return nil, mederr.New(mederr.CatIO, "read media %q", mediaPath).Wrap(err)
	}
	return ExtractMedia(data)
}

func buildOrder(metas []TrackMeta) ([]uint64, error) {
	tracks := make([]track.Track, 0, len(metas))
	for _, m := range metas {
		tracks = append(tracks, track.Track{
			ID: m.ID, UID: m.UID, Kind: track.Kind(m.Kind), CodecID: m.Codec,
			DependsOn: append([]uint64(nil), m.DependsOn...),
			ClockRefs: append([]uint64(nil), m.ClockRefs...),
		})
	}
	g, err := track.NewGraph(tracks)
	if err != nil {
		return nil, err
	}
	return g.TopoSort()
}
