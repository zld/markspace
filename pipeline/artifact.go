package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zld/media-check/media"
)

// makeHandler builds the per-segment worker. It re-derives the segment hash
// from the source and writes a compact artifact atomically; on resume, an
// existing artifact whose recorded hashes still match is accepted untouched.
func makeHandler(root, dir, source, sourceSHA string, resume bool, ledger map[int]bool) Handler {
	return func(ctx context.Context, j Job) Result {
		res := Result{
			Index: j.Index, TrackID: j.TrackID, Segment: j.Segment,
			SampleCount: j.Segment.Count, FirstSample: j.Segment.First,
			DTSStart: j.Segment.DTSStart, Duration: j.Segment.Duration,
			Bytes: j.Segment.ByteSize,
		}
		select {
		case <-ctx.Done():
			res.Status = StatusCancelled
			res.Err = ctx.Err()
			return res
		default:
		}
		artName := fmt.Sprintf("artifacts/seg-%04d.json", j.Index)
		artPath := filepath.Join(dir, fmt.Sprintf("seg-%04d.json", j.Index))
		if _, err := media.ValidateOutPath(root, artPath); err != nil {
			res.Status = StatusFailed
			res.Err = err
			return res
		}
		prev, havePrev := loadArtifact(artPath)
		if resume && havePrev && ledger != nil && ledger[j.Index] && prev.SHA256 != "" &&
			prev.FirstSample == j.Segment.First && prev.SampleCount == j.Segment.Count &&
			prev.DTSStart == j.Segment.DTSStart && prev.SourceSHA == sourceSHA {
			// The artifact must be internally consistent with the bytes just
			// loaded; a truncated or hand-edited file fails this and forces a
			// full recomputation.
			if prev.ArtifactSHA == artifactDigest(prev) {
				res.Status = StatusOK
				res.SHA256 = prev.SHA256
				res.ArtifactPath = artName
				res.ArtifactSHA = prev.ArtifactSHA
				res.Resumed = true
				return res
			}
		}
		f, err := os.Open(source)
		if err != nil {
			res.Status = StatusFailed
			res.Err = media.WrapError(media.ClassIO, "segment.open", 0, err, "open source")
			return res
		}
		defer f.Close()
		sha, err := media.HashSegment(f, j.Table, j.Segment)
		if err != nil {
			res.Status = StatusFailed
			res.Err = err
			return res
		}
		select {
		case <-ctx.Done():
			res.Status = StatusCancelled
			res.Err = ctx.Err()
			return res
		default:
		}
		res.SHA256 = sha
		art := artifactRecord{
			Index: j.Index, TrackID: j.TrackID, FirstSample: j.Segment.First,
			SampleCount: j.Segment.Count, DTSStart: j.Segment.DTSStart,
			Duration: j.Segment.Duration, ByteSize: j.Segment.ByteSize,
			SHA256: sha, SourceSHA: sourceSHA, Version: artifactVersion,
		}
		// The artifact digest excludes the digest field itself, so a single
		// write is self-describing and can be validated on resume.
		art.ArtifactSHA = artifactDigest(art)
		if err := writeJSONAtomic(dir, artPath, &art); err != nil {
			res.Status = StatusFailed
			res.Err = err
			return res
		}
		res.Status = StatusOK
		res.ArtifactPath = artName
		res.ArtifactSHA = art.ArtifactSHA
		return res
	}
}

const artifactVersion = 1

// artifactRecord is the per-segment evidence file. It stores enough to decide,
// on resume, whether the prior work is still valid. It deliberately holds no
// absolute paths so that a run is reproducible across output directories.
type artifactRecord struct {
	Version     int    `json:"version"`
	Index       int    `json:"index"`
	TrackID     uint32 `json:"track_id"`
	FirstSample uint32 `json:"first_sample"`
	SampleCount uint32 `json:"sample_count"`
	DTSStart    uint64 `json:"dts_start"`
	Duration    uint64 `json:"duration"`
	ByteSize    int64  `json:"byte_size"`
	SHA256      string `json:"sha256"`
	ArtifactSHA string `json:"artifact_sha256"`
	SourceSHA   string `json:"source_sha256"`
}

// artifactDigest hashes every content field of an artifact record, excluding
// only the digest field itself.
func artifactDigest(a artifactRecord) string {
	a.ArtifactSHA = ""
	b, err := json.Marshal(&a)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func loadArtifact(path string) (artifactRecord, bool) {
	var a artifactRecord
	b, err := os.ReadFile(path)
	if err != nil {
		return a, false
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return a, false
	}
	return a, true
}
