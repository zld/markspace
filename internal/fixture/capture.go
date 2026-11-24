package fixture

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"mediasync/internal/manifest"
	"mediasync/internal/model"
)

// CaptureConfig controls the generated capture log.
type CaptureConfig struct {
	Seed       int64
	WrapBits   uint
	TsWrapBits uint
	// DurationMS is the media span of every camera.
	DurationMS int64
	// VideoFPS is the nominal camera rate. Per-frame jitter makes the stream
	// variable-framerate; samples are never resampled onto a fixed grid.
	VideoFPS float64
	// AudioPacketsPerSecond is the audio packet cadence.
	AudioPacketsPerSecond int64
	// Cameras is the number of video tracks.
	Cameras int
	// DriftPPM is the audio clock drift applied to audio timestamps.
	DriftPPM float64
	// LossBursts is how many consecutive-loss bursts to inject.
	LossBursts int
	// LateEvents is how many packets to deliver long after their timestamp.
	LateEvents int
	// ReorderEvents is how many packets to deliver out of order within the
	// reorder window.
	ReorderEvents int
	// Duplicates is how many packets to deliver twice.
	Duplicates int
}

// DefaultCaptureConfig is the demo configuration.
func DefaultCaptureConfig(seed int64) CaptureConfig {
	return CaptureConfig{
		Seed: seed, WrapBits: 16, TsWrapBits: 32, DurationMS: 20_000,
		VideoFPS: 50, AudioPacketsPerSecond: 50, Cameras: 3,
		DriftPPM: 120, LossBursts: 6, LateEvents: 4, ReorderEvents: 10, Duplicates: 5,
	}
}

// captureTrack is the static description of one generated track.
type captureTrack struct {
	id         uint32
	kind       model.TrackKind
	timescale  uint32
	ssrc       uint32
	startTicks int64
	keyEvery   int
}

// TrackPlan is the per-track schedule used by the event writer.
type TrackPlan struct {
	Track      captureTrack
	SeqStart   uint32
	TsStart    uint64
	SampleStep float64
	Count      int
	// Ticks is the media position of each sample in track-timescale units,
	// strictly increasing so variable frame rate is preserved exactly.
	Ticks []int64
	// NominalTicks is the drift-free position used for capture arrival. The
	// host clock that stamps arrivals is shared by all tracks, so arrival
	// must be independent of each track's drifting media clock.
	NominalTicks []int64
}

// Generate writes manifest.json and capture.ndjson into dir and returns their
// paths. All randomness comes from the seed; identical seeds produce
// byte-identical files.
func Generate(dir string, cfg CaptureConfig) (manifestPath, eventsPath string, err error) {
	if cfg.Cameras <= 0 {
		return "", "", fmt.Errorf("capture needs at least one camera")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	rng := model.NewRNG(cfg.Seed)
	if cfg.WrapBits == 0 {
		cfg.WrapBits = 16
	}
	if cfg.TsWrapBits == 0 {
		cfg.TsWrapBits = 32
	}

	seqMod := uint32(1) << cfg.WrapBits
	tsMod := uint64(1) << cfg.TsWrapBits
	if cfg.WrapBits >= 32 {
		seqMod = 0
	}
	if cfg.TsWrapBits >= 64 {
		tsMod = 0
	}

	// Non-zero, but closely spaced, declared starts: the tracks begin within a
	// few milliseconds of one another, which is what a clapper-stick-aligned
	// multi-camera capture looks like before drift correction.
	videoStartMS := 1 + rng.Intn(4)
	audioStartMS := 1 + rng.Intn(4)
	tracks := make([]captureTrack, 0, cfg.Cameras+1)
	for i := 0; i < cfg.Cameras; i++ {
		tracks = append(tracks, captureTrack{
			id: uint32(i + 1), kind: model.KindVideo, timescale: 90_000,
			ssrc:       uint32(0x1111_0000 + i),
			startTicks: videoStartMS*90 + int64(rng.Intn(90)),
			keyEvery:   25,
		})
	}
	tracks = append(tracks, captureTrack{
		id: uint32(cfg.Cameras + 1), kind: model.KindAudio, timescale: 48_000,
		ssrc: 0x2222_0000, startTicks: audioStartMS*48 + int64(rng.Intn(48)),
	})

	plans := make([]TrackPlan, 0, len(tracks))
	for ti, t := range tracks {
		p := TrackPlan{Track: t}
		if t.kind == model.KindVideo {
			p.SampleStep = float64(t.timescale) / cfg.VideoFPS
			p.Count = int(float64(cfg.DurationMS) * cfg.VideoFPS / 1000)
		} else {
			p.SampleStep = float64(t.timescale) / float64(cfg.AudioPacketsPerSecond)
			p.Count = int(cfg.DurationMS * cfg.AudioPacketsPerSecond / 1000)
		}
		if p.Count <= 0 {
			p.Count = 1
		}
		// Sequence numbers start just below the wrap boundary on the first
		// track so the log exercises an actual wrap.
		if seqMod != 0 && ti == 0 {
			p.SeqStart = seqMod - 1 - uint32(rng.Intn(120))
		} else {
			p.SeqStart = uint32(rng.Intn(1000))
		}
		if tsMod != 0 {
			p.TsStart = tsMod - uint64(rng.Intn(4096)) - 1
		} else {
			p.TsStart = uint64(rng.Intn(1 << 20))
		}
		p.Ticks = make([]int64, p.Count)
		p.NominalTicks = make([]int64, p.Count)
		prevNom, prev := int64(-1), int64(-1)
		for i := 0; i < p.Count; i++ {
			nominal := int64(float64(i) * p.SampleStep)
			if nominal <= prevNom {
				nominal = prevNom + 1
			}
			prevNom = nominal
			p.NominalTicks[i] = nominal

			ticks := float64(nominal)
			if t.kind == model.KindVideo {
				// VFR: decode timestamps jitter around the nominal grid, but
				// samples are never resampled onto it.
				ticks += (rng.Float() - 0.5) * 0.35 * p.SampleStep
			} else {
				// The audio media clock runs fast by DriftPPM relative to the
				// shared capture clock.
				ticks += ticks * cfg.DriftPPM / 1e6
			}
			ti64 := int64(ticks)
			if ti64 <= prev {
				ti64 = prev + 1
			}
			prev = ti64
			p.Ticks[i] = ti64
		}
		plans = append(plans, p)
	}

	man := manifest.Manifest{Version: manifest.Version, BaseTimescale: 1_000_000_000}
	for _, p := range plans {
		durTicks := p.Ticks[len(p.Ticks)-1] + p.Ticks[0] + 1
		tk := manifest.Track{
			ID: p.Track.id, Kind: p.Track.kind, Timescale: p.Track.timescale,
			StartTicks: p.Track.startTicks, DurationTicks: durTicks,
			MediaFile: fmt.Sprintf("cam%d.mp4", p.Track.id),
			Edit:      []manifest.Edit{{StartTicks: 0, DurationTicks: durTicks, MediaTimeTick: 0}},
		}
		if p.Track.kind == model.KindVideo {
			for i := 0; i < p.Count; i += p.Track.keyEvery {
				tk.Keyframes = append(tk.Keyframes, p.Ticks[i])
			}
		} else {
			tk.Keyframes = []int64{p.Ticks[0]}
		}
		man.Tracks = append(man.Tracks, tk)
	}

	if manifestPath, err = writeManifest(dir, man); err != nil {
		return "", "", err
	}
	eventsPath, err = writeEvents(dir, cfg, plans, rng)
	return manifestPath, eventsPath, err
}

func writeManifest(dir string, man manifest.Manifest) (string, error) {
	path := filepath.Join(dir, "manifest.json")
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(man); err != nil {
		f.Close()
		return "", err
	}
	return path, f.Close()
}
