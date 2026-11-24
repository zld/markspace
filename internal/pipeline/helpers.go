package pipeline

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"mediad/internal/checkpoint"
	"mediad/internal/clock"
	"mediad/internal/fixture"
	"mediad/internal/index"
	"mediad/internal/manifest"
	"mediad/internal/metrics"
	mp4 "mediad/internal/mp4"
	"mediad/internal/provenance"
)

func prepare(opts Options) (string, map[string]string, error) {
	needGen := opts.Regenerate
	_, statErr := os.Stat(opts.ManifestPath)
	if os.IsNotExist(statErr) {
		// Only the demo generator creates a manifest. A caller-supplied path
		// that is absent is an argument error, not an invitation to generate.
		if opts.ExternalManifest {
			return "", nil, clock.New(clock.StageArgs, clock.CodeBadManifest, "manifest %s does not exist", opts.ManifestPath)
		}
		needGen = true
	}
	// An existing generated manifest is only reusable when it describes the
	// same seed and target. Otherwise the on-disk media no longer matches the
	// requested run, and reusing it would silently deliver the wrong bitrate.
	// Caller-supplied manifests are trusted as given and never regenerated.
	if !needGen && !opts.ExternalManifest {
		seed, target, corrupt, ok := manifestIdentity(opts.ManifestPath)
		if !ok || seed != opts.Seed || target != opts.TargetBps || corrupt != opts.Corrupt {
			needGen = true
		}
	}
	if needGen {
		if _, err := fixture.Generate(opts.WorkDir, opts.Seed, opts.TargetBps, opts.Corrupt); err != nil {
			return "", nil, clock.New(clock.StageArgs, clock.CodeIO, "generate fixtures: %v", err).WithCause(err)
		}
	} else if _, err := os.Stat(opts.ManifestPath); err != nil {
		return "", nil, clock.New(clock.StageArgs, clock.CodeBadManifest, "manifest missing: %v", err)
	}
	raw, err := os.ReadFile(opts.ManifestPath)
	if err != nil {
		return "", nil, clock.New(clock.StageParse, clock.CodeIO, "read manifest: %v", err)
	}
	sha, err := provenance.HashReader(bytes.NewReader(raw))
	if err != nil {
		return "", nil, err
	}
	man, err := manifest.ParseFile(opts.ManifestPath, opts.MediaRoot)
	if err != nil {
		return "", nil, err
	}
	sources := map[string]string{}
	for _, e := range man.Entries {
		h, err := provenance.HashFile(e.SourceAbs)
		if err != nil {
			return "", nil, err
		}
		sources[e.Camera] = h
	}
	return sha, sources, nil
}

// manifestIdentity reads only the fields that decide whether existing media
// matches the requested run. A manifest that predates seed recording returns
// ok=false so it is regenerated.
func manifestIdentity(path string) (seed, target int64, corruption string, ok bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, "", false
	}
	var probe struct {
		Seed       *int64 `json:"seed"`
		TargetBps  int64  `json:"target_bps"`
		Corruption string `json:"corruption"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil || probe.Seed == nil {
		return 0, 0, "", false
	}
	return *probe.Seed, probe.TargetBps, probe.Corruption, true
}

func hashSources(sources map[string]string) (string, error) {
	cams := make([]string, 0, len(sources))
	for c := range sources {
		cams = append(cams, c)
	}
	sort.Strings(cams)
	parts := make([]string, 0, len(cams)*2)
	for _, c := range cams {
		parts = append(parts, c, sources[c])
	}
	return checkpoint.Fingerprint(parts...), nil
}

// scanFiles scans every camera source.
func scanFiles(man *manifest.Manifest) (map[string]*mp4.Scan, error) {
	out := map[string]*mp4.Scan{}
	for _, e := range man.Entries {
		s, err := mp4.ScanFile(e.SourceAbs)
		if err != nil {
			return nil, err
		}
		out[e.Camera] = s
	}
	return out, nil
}

func repairAll(man *manifest.Manifest, scans map[string]*mp4.Scan, opts Options) (map[string]*mp4.Repaired, error) {
	out := map[string]*mp4.Repaired{}
	for _, e := range man.Entries {
		s := scans[e.Camera]
		if s == nil {
			continue
		}
		specs := map[uint32]mp4.TrackSpec{}
		for _, tsp := range e.Tracks {
			specs[tsp.TrackID] = mp4.TrackSpec{Timescale: tsp.Timescale, OriginNs: e.Recording.StartNs}
		}
		var rtp []mp4.RTPRecord
		if path, ok := rtpPathFor(e, opts); ok {
			recs, err := fixture.ReadRTP(path)
			if err != nil {
				return nil, clock.New(clock.StageRepair, clock.CodeIO, "read rtp sidecar: %v", err).WithCause(err)
			}
			for _, r := range recs {
				rtp = append(rtp, mp4.RTPRecord{Timestamp: r.Timestamp, CaptureNs: r.CaptureNs, SSRC: r.SSRC})
			}
		}
		rep, err := mp4.Repair(s, mp4.RepairOptions{
			Policy: opts.Policy, RTPRecords: rtp, TrackSpecs: specs,
			RecordingOriginNs: e.Recording.StartNs,
		})
		if err != nil {
			return nil, err
		}
		out[e.Camera] = rep
	}
	return out, nil
}

func rtpPathFor(e manifest.Entry, opts Options) (string, bool) {
	if opts.WorkDir == "" {
		return "", false
	}
	p := filepath.Join(opts.WorkDir, "media", e.Camera+".rtp.jsonl")
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

func buildIndexes(repairs map[string]*mp4.Repaired, opts Options) (map[string]*index.Index, error) {
	out := map[string]*index.Index{}
	for cam, rep := range repairs {
		idx, err := index.Build(rep, index.Options{InferSync: opts.InferSync})
		if err != nil {
			return nil, err
		}
		out[cam] = idx
	}
	return out, nil
}

func measure(repairs map[string]*mp4.Repaired, indexes map[string]*index.Index, man *manifest.Manifest, opts Options) *metrics.Report {
	in := metrics.Input{TargetBps: opts.TargetBps,
		Tracks:  map[string]*mp4.RepairedTrack{},
		Indexes: map[string]*index.TrackIndex{}}
	for _, e := range man.Entries {
		rep := repairs[e.Camera]
		if rep == nil || len(rep.Tracks) == 0 {
			continue
		}
		in.Tracks[e.Camera] = &rep.Tracks[0]
		if idx := indexes[e.Camera]; idx != nil && len(idx.Tracks) > 0 {
			in.Indexes[e.Camera] = &idx.Tracks[0]
		}
		// Manifest windows are session-relative offsets; convert to the
		// camera's frame-relative space.
		in.Windows = append(in.Windows, metrics.Window{
			Camera:  e.Camera,
			StartNs: e.Editorial.StartNs - e.Recording.StartNs,
			EndNs:   e.Editorial.EndNs - e.Recording.StartNs,
		})
	}
	return metrics.Compute(in)
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return clock.New(clock.StageEmit, clock.CodeIO, "marshal %s: %v", path, err).WithCause(err)
	}
	return provenance.AtomicWrite(path, append(data, '\n'))
}
