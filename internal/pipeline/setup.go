package pipeline

import (
	"context"
	"strings"

	"mediapack/internal/index"
	"mediapack/internal/recovery"
)

func loadCheckpoint(_ context.Context, opts Options, fx *Fixture, identity string) (*recovery.Checkpoint, error) {
	path := recovery.CheckpointPath(opts.Out)
	if opts.Resume {
		return recovery.LoadOrNew(opts.Out, fx.SourceSHA256, identity, "multi-platform", fx.LayerNames())
	}
	cp, err := recovery.Load(path)
	if err == nil && cp.InputSHA256 == fx.SourceSHA256 && cp.ParamsSHA256 == identity {
		return cp, nil
	}
	cp = recovery.NewCheckpoint(opts.Out, fx.SourceSHA256, identity, "multi-platform", fx.LayerNames())
	if err := cp.Save(); err != nil {
		return nil, err
	}
	return cp, nil
}

func prepareSubtitles(fx *Fixture, out string, specs []index.Spec, decisions map[string]index.Decision, store *index.Store) error {
	needWrite := false
	for _, id := range []string{"subtitle:srt", "subtitle:vtt"} {
		if d := decisions[id]; d.Reason != "digests_match" {
			needWrite = true
		}
	}
	paths, hashes, err := writeSubtitleAssets(fx, out)
	if err != nil {
		return err
	}
	if !needWrite {
		return nil
	}
	for _, spec := range specs {
		if !strings.HasPrefix(spec.ID, "subtitle:") {
			continue
		}
		format := strings.TrimPrefix(spec.ID, "subtitle:")
		store.Commit(index.Record{ID: spec.ID, Key: spec.Key, Outputs: map[string]string{
			"path":   paths[format],
			"sha256": hashes[format],
		}})
	}
	return store.Save()
}

func decisionIDs(in []index.Decision) []string {
	out := make([]string, 0, len(in))
	for _, d := range in {
		out = append(out, d.ID)
	}
	return out
}
