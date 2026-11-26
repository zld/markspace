package pipeline

import (
	"encoding/hex"
	"encoding/json"
	"io"
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

		// A verified cache entry is streamed back to the output path and
		// re-verified there; any failure invalidates the entry for rebuild.
		if cachePath, ent, outcome := store.Get(key, inputSHA, "video/mp4"); outcome == cache.HitVerified {
			copyErr := streamCopy(cachePath, outPath)
			if copyErr == nil && cache.VerifyArtifact(outPath, ent.ArtifactSHA, ent.Size) == cache.HitVerified {
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
		resultJSON, err := json.Marshal(res)
		if err != nil {
			return nil, err
		}
		// Stream the finished clip into the cache with bounded memory; the
		// store hashes bytes as they pass through.
		in, err := os.Open(outPath)
		if err != nil {
			return nil, err
		}
		ent := cache.Entry{
			Key: key, SourceSHA: inputSHA, ConfigSHA: configSHA,
			MediaType: "video/mp4", Result: resultJSON,
		}
		putErr := store.PutStream(ent, in)
		in.Close()
		if putErr != nil {
			return nil, putErr
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

// streamCopy copies a cached artifact to its output path using a bounded
// 64 KiB buffer so neither file is fully buffered in memory.
func streamCopy(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	buf := make([]byte, 64*1024)
	_, err = io.CopyBuffer(out, in, buf)
	return err
}
