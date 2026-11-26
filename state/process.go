package state

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"archivecheck/cache"
	"archivecheck/checkpoint"
	"archivecheck/manifest"
	"archivecheck/pipe"
	rtpingest "archivecheck/rtp"
)

func (e *engine) handle(ctx context.Context, rec pipe.Record) error {
	if err := sleepSlow(ctx, e.cfg.SlowMS); err != nil {
		return err
	}
	wi, ok := e.byCursor[rec.Cursor]
	if !ok {
		return os.ErrNotExist
	}

	var digest, stage string
	var err error
	switch rec.Kind {
	case "dash-group":
		digest, stage, err = e.processDASHGroup(ctx, rec.Cursor, wi.dashIndex)
	case "hls-segment":
		digest, stage, err = e.processHLS(ctx, rec.Cursor, wi.hlsIndex)
	case "rtp-event":
		digest, stage, err = e.processEvent(rec.Cursor, wi.event)
	default:
		err = os.ErrInvalid
	}
	if err != nil {
		return err
	}
	if stage == "" {
		stage = checkpoint.StageVerified
	}
	if err := e.journal.AppendStage(ctx, checkpoint.StageRecord{
		Offset: rec.Cursor, InputDigest: e.inputDigest, Stage: stage,
		Cursor: rec.Cursor, Source: rec.Source, Detail: digest,
	}); err != nil {
		return err
	}

	// Fault injection point: object is already in CAS, but checkpoint.json has
	// not yet been replaced. A real crash has the same durable state.
	if e.cfg.InterruptCursor == rec.Cursor {
		return pipe.ErrHalt
	}
	return e.persist(ctx, rec.Cursor)
}

func sleepSlow(ctx context.Context, ms int) error {
	if ms <= 0 {
		return nil
	}
	t := time.NewTimer(time.Duration(ms) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *engine) persist(ctx context.Context, cursor int64) error {
	if err := e.writeArchive(); err != nil {
		return err
	}
	return e.journal.Save(ctx, checkpoint.Checkpoint{
		InputDigest: e.inputDigest,
		Stage:       checkpoint.StageCheckpoint,
		Cursor:      cursor,
	})
}

func (e *engine) sourceReader(rel string) cache.ReadFunc {
	abs := filepath.Join(e.root, filepath.FromSlash(rel))
	return func(context.Context) (cache.Snapshot, error) {
		b, err := os.ReadFile(abs)
		if err != nil {
			return cache.Snapshot{}, err
		}
		return cache.Snapshot{SourceID: rel, Data: b}, nil
	}
}

func (e *engine) processDASHGroup(ctx context.Context, cursor int64, idx int) (string, string, error) {
	failures := 0
	for _, rep := range e.dash.Representations {
		if idx >= len(rep.Segments) {
			continue
		}
		rel := rep.Segments[idx].URL
		res, data, err := e.cas.Ensure(ctx, rel, "", e.sourceReader(rel))
		if err != nil {
			return "", "", err
		}
		if _, err := manifest.InspectFMP4(data, rel); err != nil {
			e.badSegments++
			failures++
			continue
		}
		status, stage := "archived", checkpoint.StageCached
		if failures > 0 {
			status, stage = "fallback", checkpoint.StageRecovered
			e.fallback++
		}
		e.dashResolved++
		e.upsertItem(ArchiveItem{
			Cursor: cursor, Kind: "dash", Source: rel, Digest: res.Entry.Digest,
			Status: status, Detail: rep.ID,
		})
		return res.Entry.Digest, stage, nil
	}

	e.unresolved++
	e.upsertItem(ArchiveItem{
		Cursor: cursor, Kind: "dash", Source: e.byCursor[cursor].rec.Source,
		Status: "unresolved", Detail: "all representations invalid",
	})
	return "", checkpoint.StageIsolated, nil
}

func (e *engine) processHLS(ctx context.Context, cursor int64, idx int) (string, string, error) {
	seg := e.hls.Segments[idx]
	rel := seg.URI
	res, data, err := e.cas.Ensure(ctx, rel, "", e.sourceReader(rel))
	if err != nil {
		return "", "", err
	}
	if _, err := manifest.InspectFMP4(data, rel); err != nil {
		e.badSegments++
		e.unresolved++
		e.upsertItem(ArchiveItem{
			Cursor: cursor, Kind: "hls", Source: rel, Status: "isolated",
			Detail: "fMP4 validation failed",
		})
		return "", checkpoint.StageIsolated, nil
	}

	e.upsertItem(ArchiveItem{
		Cursor: cursor, Kind: "hls", Source: rel, Digest: res.Entry.Digest, Status: "archived",
	})
	if idx == e.hlsNext {
		e.hlsNext++
	}
	return res.Entry.Digest, checkpoint.StageCached, nil
}

func (e *engine) processEvent(cursor int64, ev *rtpingest.Event) (string, string, error) {
	obs, err := e.tracker.Observe(*ev)
	if err != nil {
		return "", "", err
	}
	if ev.Kind == "audio" {
		e.audioPTS = append(e.audioPTS, ev.PTSNS)
	} else if ev.Kind == "video" {
		e.videoPTS = append(e.videoPTS, ev.PTSNS)
	}
	e.upsertItem(ArchiveItem{
		Cursor: cursor, Kind: ev.Kind, Source: ev.Source + "/" + ev.Track,
		Digest: ev.Digest, Status: string(obs.Disposition),
	})
	return ev.Digest, checkpoint.StageVerified, nil
}
