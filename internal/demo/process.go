package demo

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"mediapipeline/internal/evidence"
	"mediapipeline/internal/index"
	mp4media "mediapipeline/internal/mp4"
	"mediapipeline/internal/pipe"
	"mediapipeline/internal/recovery"
	"mediapipeline/internal/rewrap"
	"mediapipeline/pkg/media"
)

// PublishResult is the verifiable outcome for the main normal asset.
type PublishResult struct {
	Key         string
	ProductPath string
	OutputSHA   string
	PipeStats   pipe.Stats
	Verify      rewrap.Verification
	Decision    index.Decision
	Reused      bool
}

func processNormal(
	cfg Config,
	files *InputFiles,
	store *index.Store,
	journal *recovery.Journal,
	snapshots *index.SnapshotStore,
	crashLeft *int,
) (*PublishResult, *media.ContainerInfo, error) {
	info, err := mp4media.ScanFile(files.Normal, cfg.ChunkBytes)
	if err != nil {
		return nil, nil, err
	}
	if info.Report.Corrupt() {
		return nil, info, fmt.Errorf("%w: normal fixture reported faults", ErrCorruptInputs)
	}
	if len(info.Tracks) == 0 {
		return nil, info, errors.New("normal fixture has no tracks")
	}

	snap := index.SnapshotFromInfo(info)
	old, hadOld := snapshots.Get(normalName)
	decision := index.Diff(nil, snap)
	if hadOld {
		decision = index.Diff(&old, snap)
	}
	if err := snapshots.Put(normalName, snap); err != nil {
		return nil, info, err
	}

	pipeStats, err := streamTrack(info.Tracks[0], cfg)
	if err != nil {
		return nil, info, err
	}

	outBytes, err := rewrap.Rewrap(info)
	if err != nil {
		return nil, info, err
	}
	key := index.ProductKey(info.SourceSHA256, "canonical-rewrap-v1", fmt.Sprintf("chunk=%d", cfg.ChunkBytes))
	productDir := filepath.Join(cfg.Work, "products")
	target := filepath.Join(productDir, key+".mp4")

	keyMu := store.LockKey(key)
	keyMu.Lock()
	defer keyMu.Unlock()

	result := &PublishResult{Key: key, ProductPath: target, PipeStats: pipeStats, Decision: decision}
	if existing, ok := store.Get(key); ok && existing.State == index.StatePublished {
		if got, _, hashErr := evidence.SHA256File(target); hashErr == nil && got == existing.OutputSHA256 {
			outInfo, scanErr := mp4media.ScanFile(target, cfg.ChunkBytes)
			if scanErr != nil {
				return result, info, scanErr
			}
			result.Verify = rewrap.Compare(info, outInfo)
			result.OutputSHA = got
			result.Reused = true
			return result, info, nil
		} else if hashErr == nil {
			_ = os.Remove(target)
		}
	}

	if _, _, err := store.EnsurePending(index.Artifact{
		Key:          key,
		SourcePath:   files.Normal,
		SourceSHA256: info.SourceSHA256,
		TrackSHA256:  info.Tracks[0].TrackSHA256,
	}); err != nil {
		return result, info, err
	}

	tmp, digest, size, err := recovery.WriteTempSynced(productDir, ".mediapipeline-tmp-*", bytes.NewReader(outBytes))
	if err != nil {
		return result, info, err
	}
	intent, err := journal.Prepare(recovery.Intent{
		Key:        key,
		SourcePath: files.Normal,
		TempPath:   tmp,
		TargetPath: target,
		SHA256:     digest,
		Size:       size,
	})
	if err != nil {
		return result, info, err
	}
	if err := recovery.Publish(tmp, target); err != nil {
		return result, info, err
	}
	result.OutputSHA = digest

	if crashLeft != nil && *crashLeft > 0 {
		*crashLeft--
		if *crashLeft == 0 {
			return result, info, ErrInjectedCrash
		}
	}

	if _, err := store.MarkPublished(key, target, digest, size); err != nil && !errors.Is(err, index.ErrAlreadyPublished) {
		return result, info, err
	}
	if err := journal.Commit(intent.ID); err != nil {
		return result, info, err
	}

	outInfo, err := mp4media.ScanFile(target, cfg.ChunkBytes)
	if err != nil {
		return result, info, err
	}
	if outInfo.Report.Corrupt() {
		return result, info, fmt.Errorf("rewrapped product is corrupt: %+v", outInfo.Report.Faults)
	}
	result.Verify = rewrap.Compare(info, outInfo)
	return result, info, nil
}
