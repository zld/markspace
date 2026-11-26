package job

import (
	"fmt"

	"mediad/internal/manifest"
	"mediad/internal/media"
)

// RecoveryReport describes what a recovery pass found and did.
type RecoveryReport struct {
	TaskKey        string
	Verified       int
	FailedHashes   []string
	ManifestDigest string
	Published      bool
}

// Recover re-verifies every archived object for a task against its recorded
// SHA-256. If any object fails verification the manifest is NOT published and
// a StageRecover error is returned, satisfying the rule that corrupt archives
// can never produce a complete manifest.
func Recover(store *Store, archive *Archive, key string) (*RecoveryReport, error) {
	objs, err := store.Objects(key)
	if err != nil {
		return nil, err
	}
	rep := &RecoveryReport{TaskKey: key}
	entries := make([]manifest.Entry, 0, len(objs))
	for _, o := range objs {
		if verr := archive.Verify(o.Hash); verr != nil {
			rep.FailedHashes = append(rep.FailedHashes, o.Hash)
			_ = store.RecordFailure(key, string(media.StageRecover), -1, verr.Error())
			continue
		}
		rep.Verified++
		entries = append(entries, manifest.Entry{
			TaskKey: key, TrackKey: o.TrackKey, SampleIdx: o.SampleIdx,
			ObjectHash: o.Hash, Size: o.Size,
		})
	}
	if len(rep.FailedHashes) > 0 {
		return rep, media.NewError(media.StageRecover, "recover",
			fmt.Sprintf("%d archived object(s) failed SHA-256 verification",
				len(rep.FailedHashes)))
	}
	merged, err := manifest.Merge(entries)
	if err != nil {
		return rep, media.NewError(media.StageRecover, "manifest.merge", err.Error())
	}
	rep.ManifestDigest = manifest.Digest(merged)
	rep.Published = true
	if err := store.SetManifest(key, rep.ManifestDigest); err != nil {
		return rep, err
	}
	return rep, nil
}

// RearchiveObject replaces an object whose on-disk content was corrupted:
// it writes the supplied good payload, verifies the result matches the
// expected hash, and only then reports success. This backs the
// "repairable, then re-run" acceptance behavior.
func RearchiveObject(archive *Archive, expectedHash string, payload []byte) error {
	hash, _, err := archive.Put(payload)
	if err != nil {
		return media.NewError(media.StageRecover, "rearchive.put", err.Error())
	}
	if hash != expectedHash {
		return media.NewError(media.StageRecover, "rearchive",
			fmt.Sprintf("rebuilt hash %s != expected %s", hash, expectedHash))
	}
	return archive.Verify(expectedHash)
}
