package pipeline

import (
	"encoding/hex"
	"encoding/json"
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
				res, rerr := decodeResult(pe, name, ent)
				if rerr != nil {
					return nil, rerr
				}
				st.CacheHits++
				st.Accepted++
				st.ClipS += secondsOf(res.Duration, res.Timescale)
				st.DeviationS = pe.deviation
				results = append(results, res)
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
		resultJSON, err := json.Marshal(res)
		if err != nil {
			return nil, err
		}
		ent := cache.Entry{
			Key: key, SourceSHA: inputSHA, ConfigSHA: configSHA,
			MediaType: "video/mp4", Result: resultJSON,
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

// decodeResult restores the full cut provenance (including sample offsets)
// from a verified cache record.
func decodeResult(pe *plannedEvent, name string, ent cache.Entry) (*clip.ClipResult, error) {
	var res clip.ClipResult
	if err := json.Unmarshal(ent.Result, &res); err != nil {
		return nil, err
	}
	res.FileName = name
	res.SHA256Hex = ent.ArtifactSHA
	res.Size = ent.Size
	return &res, nil
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
