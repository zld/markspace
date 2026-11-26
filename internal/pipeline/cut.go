package pipeline

import (
	"encoding/hex"
	"os"
	"path/filepath"

	"replay/internal/cache"
	"replay/internal/clip"
	"replay/internal/events"
)

// StopAfter signals a deliberate --stop-after interruption. It is not a
// failure; the CLI maps it to exit code 75.
type StopAfter struct{ stage string }

// NewStopAfter builds the interruption sentinel for the reached stage.
func NewStopAfter(stage string) *StopAfter { return &StopAfter{stage: stage} }

func (e *StopAfter) Error() string { return "stopped after stage " + e.stage }

// Stage returns the stage that was reached.
func (e *StopAfter) Stage() string { return e.stage }

// cutPlans materializes every still-accepted plan. Cache entries are keyed
// by source+config and reused only when the on-disk artifact matches the
// stored digest and length; tampered or stale entries are rebuilt.
func cutPlans(c *Config, b *clip.Builder, store *cache.Store,
	plans []*plannedEvent, inputSHA, configSHA string, st *Stats) ([]*clip.ClipResult, error) {

	clipsDir := filepath.Join(c.OutDir, "clips")
	if err := os.MkdirAll(clipsDir, 0o755); err != nil {
		return nil, err
	}
	var results []*clip.ClipResult
	for _, pe := range plans {
		if pe.decision != events.DecisionAccepted {
			continue
		}
		name := "clip-" + pe.ev.ID + ".mp4"
		outPath := filepath.Join(clipsDir, name)
		sourceSum := mustSHA(inputSHA)
		key := cache.Key(sourceSum, configSHA+":"+pe.ev.ID)

		// A previously delivered clip is trusted only if it still verifies
		// against both the cache record and the materialized output file.
		if _, ent, outcome := store.Get(key, inputSHA, "video/mp4"); outcome == cache.HitVerified {
			if cache.VerifyArtifact(outPath, ent.ArtifactSHA, ent.Size) == cache.HitVerified {
				st.CacheHits++
				results = append(results, resultFromCache(pe, name, ent))
				continue
			}
			st.CacheRebuilds++
		} else if outcome != cache.HitMiss {
			st.CacheRebuilds++
		}

		res, err := b.Cut(pe.spec, outPath)
		if err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(outPath)
		if err != nil {
			return nil, err
		}
		ent := cache.Entry{
			Key: key, SourceSHA: inputSHA, ConfigSHA: configSHA,
			MediaType: "video/mp4",
		}
		if err := store.Put(ent, raw); err != nil {
			return nil, err
		}
		st.Accepted++
		st.ClipS += secondsOf(res.Duration, res.Timescale)
		st.DeviationS = pe.deviation
		results = append(results, res)
	}
	return results, nil
}

// resultFromCache reconstructs the manifest result from a verified record.
func resultFromCache(pe *plannedEvent, name string, ent cache.Entry) *clip.ClipResult {
	return &clip.ClipResult{
		EventID: pe.ev.ID, FileName: name,
		SHA256Hex: ent.ArtifactSHA, Size: ent.Size,
		StartPTS: pe.spec.SyncStart, Duration: pe.clipDur,
		Timescale: pe.timescale(),
	}
}

func mustSHA(hexSHA string) [32]byte {
	var out [32]byte
	raw, err := hex.DecodeString(hexSHA)
	if err != nil {
		panic(err)
	}
	copy(out[:], raw)
	return out
}

func secondsOf(ticks int64, timescale uint32) float64 {
	return float64(ticks) / float64(timescale)
}
