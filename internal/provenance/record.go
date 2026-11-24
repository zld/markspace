// Package provenance produces the delivery record: input digest, manifest
// line, repair intervals, index version and per-stage output digests. Records
// are deterministic so two identical runs yield identical digests.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"

	"mediad/internal/clock"
	"mediad/internal/index"
	mp4 "mediad/internal/mp4"
)

// StageRecord captures one pipeline stage's inputs and outputs.
type StageRecord struct {
	Stage     string `json:"stage"`
	Version   int    `json:"version"`
	InputSHA  string `json:"input_sha256"`
	ParamSHA  string `json:"param_sha256"`
	OutputSHA string `json:"output_sha256"`
	Status    string `json:"status"`
	Resumed   bool   `json:"resumed"`
	Detail    string `json:"detail,omitempty"`
	ElapsedMs int64  `json:"elapsed_ms,omitempty"`
}

// CameraRecord ties a delivered angle back to its manifest line and source.
type CameraRecord struct {
	Camera        string               `json:"camera"`
	File          string               `json:"file"`
	ManifestLine  int                  `json:"manifest_line"`
	InputSHA      string               `json:"input_sha256"`
	TrackID       uint32               `json:"track_id"`
	Timescale     uint32               `json:"timescale"`
	EditList      clock.EditList       `json:"edit_list"`
	Repairs       []mp4.RepairInterval `json:"repairs"`
	IndexVersion  int                  `json:"index_version"`
	IndexSHA      string               `json:"index_sha256"`
	SyncFromFlags bool                 `json:"sync_from_flags"`
	DerivedSync   bool                 `json:"derived_sync"`
}

// Record is the whole delivery provenance document.
type Record struct {
	Version     int            `json:"version"`
	Seed        int64          `json:"seed"`
	TargetBps   int64          `json:"target_bps"`
	ManifestSHA string         `json:"manifest_sha256"`
	Stages      []StageRecord  `json:"stages"`
	Cameras     []CameraRecord `json:"cameras"`
	// Digest is the reproducible delivery digest. It deliberately excludes
	// Resumed and ResumeNotes, which are run state rather than content, so a
	// resumed rerun and a cold run produce the same Digest.
	Digest      string   `json:"digest"`
	Resumed     bool     `json:"resumed"`
	ResumeNotes []string `json:"resume_notes,omitempty"`
}

// Version is the provenance schema version.
const Version = 1

// HashFile computes the SHA-256 of a file with a bounded buffer.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", clock.New(clock.StageEmit, clock.CodeIO, "open %s: %v", path, err).WithCause(err)
	}
	defer f.Close()
	return HashReader(f)
}

// HashReader computes the SHA-256 of r without buffering it whole.
func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.CopyBuffer(h, r, make([]byte, 256*1024)); err != nil {
		return "", clock.New(clock.StageEmit, clock.CodeIO, "hash: %v", err).WithCause(err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashJSON produces a stable digest of any JSON-serializable value.
func HashJSON(v any) (string, []byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", nil, clock.New(clock.StageEmit, clock.CodeBadArgs, "marshal: %v", err).WithCause(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), data, nil
}

// BuildCamera assembles the per-camera record from pipeline outputs.
func BuildCamera(camera, file string, line int, inputSHA string, tr *mp4.RepairedTrack, ti *index.TrackIndex) (CameraRecord, error) {
	if tr == nil || ti == nil {
		return CameraRecord{}, clock.New(clock.StageEmit, clock.CodeBadArgs, "nil track or index for %s", camera)
	}
	idxSHA, _, err := HashJSON(ti)
	if err != nil {
		return CameraRecord{}, err
	}
	repairs := make([]mp4.RepairInterval, 0, len(tr.Intervals))
	for _, iv := range tr.Intervals {
		// Copy only fields that affect delivery, in a stable order.
		repairs = append(repairs, iv)
	}
	return CameraRecord{
		Camera: camera, File: file, ManifestLine: line, InputSHA: inputSHA,
		TrackID: tr.TrackID, Timescale: tr.Timescale, EditList: tr.EditList,
		Repairs: repairs, IndexVersion: index.Version, IndexSHA: idxSHA,
		SyncFromFlags: tr.SyncFromFlags, DerivedSync: ti.DerivedSync,
	}, nil
}

// digestView is the canonical subset that defines delivered content. Run
// state (Resumed, ResumeNotes, per-stage Resumed) is excluded so identical
// inputs hash identically whether or not checkpoints were reused.
type digestView struct {
	Version     int            `json:"version"`
	Seed        int64          `json:"seed"`
	TargetBps   int64          `json:"target_bps"`
	ManifestSHA string         `json:"manifest_sha256"`
	Stages      []stageView    `json:"stages"`
	Cameras     []CameraRecord `json:"cameras"`
}

type stageView struct {
	Stage     string `json:"stage"`
	Version   int    `json:"version"`
	InputSHA  string `json:"input_sha256"`
	ParamSHA  string `json:"param_sha256"`
	OutputSHA string `json:"output_sha256"`
	Status    string `json:"status"`
}

// Finalize computes the reproducible digest over the canonical content view.
func (r *Record) Finalize() (string, error) {
	r.Version = Version
	v := digestView{
		Version: Version, Seed: r.Seed, TargetBps: r.TargetBps,
		ManifestSHA: r.ManifestSHA, Cameras: r.Cameras,
	}
	for _, s := range r.Stages {
		if s.Stage == "resume" {
			continue
		}
		v.Stages = append(v.Stages, stageView{
			Stage: s.Stage, Version: s.Version, InputSHA: s.InputSHA,
			ParamSHA: s.ParamSHA, OutputSHA: s.OutputSHA, Status: s.Status,
		})
	}
	sha, _, err := HashJSON(v)
	if err != nil {
		return "", err
	}
	r.Digest = sha
	return sha, nil
}

// Write emits the record as indented JSON through an atomic rename.
func (r *Record) Write(path string) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return clock.New(clock.StageEmit, clock.CodeIO, "marshal provenance: %v", err).WithCause(err)
	}
	return AtomicWrite(path, append(data, '\n'))
}

// AtomicWrite writes data to path via temp file + rename in the same dir.
func AtomicWrite(path string, data []byte) error {
	dir := dirOf(path)
	tmp, err := os.CreateTemp(dir, ".provenance-*")
	if err != nil {
		return clock.New(clock.StageEmit, clock.CodeIO, "temp: %v", err).WithCause(err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return clock.New(clock.StageEmit, clock.CodeIO, "write: %v", err).WithCause(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return clock.New(clock.StageEmit, clock.CodeIO, "sync: %v", err).WithCause(err)
	}
	if err := tmp.Close(); err != nil {
		return clock.New(clock.StageEmit, clock.CodeIO, "close: %v", err).WithCause(err)
	}
	if err := os.Rename(name, path); err != nil {
		return clock.New(clock.StageEmit, clock.CodeIO, "rename: %v", err).WithCause(err)
	}
	return nil
}

func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if os.IsPathSeparator(path[i]) {
			if i == 0 {
				return string(os.PathSeparator)
			}
			return path[:i]
		}
	}
	return "."
}
