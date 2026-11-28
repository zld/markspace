// Package report turns pipeline outcomes into a deterministic, quantised
// summary and commits it through a staging area. A run either commits its
// complete output or rolls the staged output back and records why; no partial
// result is ever presented as confirmed.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediapipe/internal/model"
)

// Rollback states.
const (
	StateCommitted  = "committed"
	StateRolledBack = "rolled_back"
)

// Status is the terminal state of a run.
type Status struct {
	State   string
	Reason  string
	Records []Record
}

// Record is one rollback event. It is intentionally free of wall-clock time so
// identical inputs produce identical rollback records.
type Record struct {
	Asset   string `json:"asset"`
	Version string `json:"version,omitempty"`
	Track   string `json:"track,omitempty"`
	Stage   string `json:"stage"`
	Kind    string `json:"kind"`
	Offset  int64  `json:"offset,omitempty"`
	Reason  string `json:"reason"`
}

// FromError derives a rollback record from a structured error.
func FromError(err error) Record {
	me := model.AsError(err)
	rec := Record{
		Asset: me.Asset, Version: me.Version, Track: me.Track,
		Stage: string(me.Stage), Kind: me.Kind.String(), Reason: me.Error(),
	}
	if me.HasOff {
		rec.Offset = me.Offset
	}
	return rec
}

// Digest is a stable content hash of one committed artefact.
type Digest struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Metrics is the quantised result set. Every numeric value is an integer in
// its natural unit so repeated runs hash identically; presentation converts to
// milliseconds or a percentage.
type Metrics struct {
	// AVSyncErrorUS is the audio/video alignment error in microseconds.
	AVSyncErrorUS int64 `json:"av_sync_error_us"`
	// LossObserved counts gaps that were still inside the reorder window;
	// LossRecovered counts those repaired by reordering. LossBeyondWindow
	// counts gaps that had already left the window and are by definition
	// unrecoverable, so they are reported but excluded from the rate.
	LossObserved     int64 `json:"loss_observed"`
	LossRecovered    int64 `json:"loss_recovered"`
	LossBeyondWindow int64 `json:"loss_beyond_window"`
	// LatencyP95US is the modelled end-to-end P95 queueing latency.
	LatencyP95US int64 `json:"latency_p95_us"`
	LatencyMaxUS int64 `json:"latency_max_us"`
	// Missing and Conflicts are itemised problem counts.
	Missing   int `json:"missing"`
	Conflicts int `json:"conflicts"`
	// UnresolvedConflicts must be zero for a run to commit.
	UnresolvedConflicts int `json:"unresolved_conflicts"`
	// RecoveredIndexes counts assets whose index had to be rebuilt.
	RecoveredIndexes int `json:"recovered_indexes"`
	// Packets and Duplicates describe the RTP window outcome.
	Packets    int `json:"packets"`
	Duplicates int `json:"duplicates"`
	// BytesScanned is the total media scanned, for memory accounting.
	BytesScanned int64 `json:"bytes_scanned"`
}

// AVSyncErrorMS is the reported A/V error in whole milliseconds.
func (m Metrics) AVSyncErrorMS() int64 { return (m.AVSyncErrorUS + 500) / 1000 }

// LossRecoveryPPM is recovered loss in parts per million, rounded half up.
func (m Metrics) LossRecoveryPPM() int64 {
	if m.LossObserved == 0 {
		return 1_000_000
	}
	return (m.LossRecovered*2_000_000 + m.LossObserved) / (2 * m.LossObserved)
}

// LossRecoveryRate is the exact ratio for display only.
func (m Metrics) LossRecoveryRate() float64 {
	if m.LossObserved == 0 {
		return 1
	}
	return float64(m.LossRecovered) / float64(m.LossObserved)
}

// LatencyP95MS is the reported P95 latency rounded to 0.1 ms.
func (m Metrics) LatencyP95MS() float64 {
	return float64(m.LatencyP95US) / 1000
}

// Summary is the complete report document.
type Summary struct {
	Seed     int64    `json:"seed"`
	Versions int      `json:"versions"`
	Window   int      `json:"window"`
	Metrics  Metrics  `json:"metrics"`
	Digests  []Digest `json:"digests"`
	Status   Status   `json:"status"`
}

// ManifestDigest is a stable hash of the committed summary, excluding nothing:
// because Status and Metrics are deterministic, an identical run yields an
// identical digest.
func (s *Summary) ManifestDigest() (string, error) {
	c := *s
	c.Digests = append([]Digest(nil), s.Digests...)
	sort.Slice(c.Digests, func(i, j int) bool { return c.Digests[i].Name < c.Digests[j].Name })
	c.Status.Records = append([]Record(nil), s.Status.Records...)
	sort.Slice(c.Status.Records, func(i, j int) bool {
		a, b := c.Status.Records[i], c.Status.Records[j]
		if a.Asset != b.Asset {
			return a.Asset < b.Asset
		}
		if a.Stage != b.Stage {
			return a.Stage < b.Stage
		}
		return a.Offset < b.Offset
	})
	data, err := json.Marshal(c)
	if err != nil {
		return "", model.New(model.KindInternal, model.StageReport, "", "digest", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// ContentDigest hashes an arbitrary canonical payload.
func ContentDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// AddDigest records a named digest, replacing any previous value.
func (s *Summary) AddDigest(name string, payload []byte) {
	d := Digest{Name: name, Value: ContentDigest(payload)}
	for i := range s.Digests {
		if s.Digests[i].Name == name {
			s.Digests[i] = d
			return
		}
	}
	s.Digests = append(s.Digests, d)
}

// committed reports whether the run may present results as confirmed.
func (s *Summary) committed() bool {
	return s.Status.State == StateCommitted && len(s.Status.Records) == 0
}

// Rollback records a failure and prevents this summary from being committed.
func (s *Summary) Rollback(err error) {
	s.Status.State = StateRolledBack
	s.Status.Reason = "run did not complete"
	if err != nil {
		s.Status.Reason = err.Error()
		s.Status.Records = append(s.Status.Records, FromError(err))
	}
	// Unconfirmed output is discarded: digests describe only confirmed data.
	s.Digests = nil
}

// RollbackReason records a policy failure without an error value.
func (s *Summary) RollbackReason(asset, stage, reason string) {
	s.Status.State = StateRolledBack
	s.Status.Reason = reason
	s.Status.Records = append(s.Status.Records, Record{
		Asset: asset, Stage: stage, Kind: model.KindUnrecoverable.String(), Reason: reason,
	})
	s.Digests = nil
}

// Commit finalises the summary for delivery.
func (s *Summary) Commit() { s.Status.State = StateCommitted }

// Stager commits payloads atomically. A staged payload becomes visible only
// after Commit; Discard removes it without publishing anything.
type Stager struct {
	dir    string
	staged map[string]string // logical name -> temp path
}

// NewStager creates an isolated staging directory.
func NewStager(dir string) (*Stager, error) {
	if dir == "" {
		d, err := os.MkdirTemp("", "mediapipe-stage-*")
		if err != nil {
			return nil, model.New(model.KindInternal, model.StageReport, "", "stage", err)
		}
		dir = d
	} else if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, model.New(model.KindInternal, model.StageReport, "", "stage", err)
	}
	return &Stager{dir: dir, staged: map[string]string{}}, nil
}

// Dir returns the staging directory.
func (s *Stager) Dir() string { return s.dir }

// Stage writes payload to a temporary file inside the staging area and returns
// the digest of the staged bytes. Nothing is visible at the final path yet.
func (s *Stager) Stage(name string, payload []byte) (string, error) {
	tmp, err := os.CreateTemp(s.dir, filepath.Base(name)+".*.tmp")
	if err != nil {
		return "", model.New(model.KindInternal, model.StageReport, name, "stage", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", model.New(model.KindInternal, model.StageReport, name, "stage", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return "", model.New(model.KindInternal, model.StageReport, name, "stage", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return "", model.New(model.KindInternal, model.StageReport, name, "stage", err)
	}
	s.staged[name] = tmpName
	return ContentDigest(payload), nil
}

// Commit atomically moves every staged file to its final name.
func (s *Stager) Commit(finalDir string, names []string) error {
	if err := os.MkdirAll(finalDir, 0o755); err != nil {
		return model.New(model.KindInternal, model.StageReport, "", "commit", err)
	}
	for _, name := range names {
		tmp, ok := s.staged[name]
		if !ok {
			return model.New(model.KindInternal, model.StageReport, name, "commit",
				fmt.Errorf("no staged payload for %q", name))
		}
		if err := os.Rename(tmp, filepath.Join(finalDir, name)); err != nil {
			return model.New(model.KindInternal, model.StageReport, name, "commit", err)
		}
		delete(s.staged, name)
	}
	return nil
}

// Discard removes the whole staging directory without publishing anything.
func (s *Stager) Discard() error {
	if s.dir == "" {
		return nil
	}
	return os.RemoveAll(s.dir)
}

// Files returns the currently staged file names, sorted.
func (s *Stager) Files() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// CommitResult verifies a summary is committable, then stages and commits it.
// Any failure leaves the staging area untouched and the summary rolled back.
func (s *Stager) CommitResult(sm *Summary, name string) (string, error) {
	if !sm.committed() {
		return "", model.New(model.KindConflict, model.StageReport, "", "commit",
			errors.New("summary is not in a committable state"))
	}
	digest, err := sm.ManifestDigest()
	if err != nil {
		return "", err
	}
	sm.AddDigest(name, []byte(digest))
	payload, err := json.MarshalIndent(sm, "", "  ")
	if err != nil {
		return "", model.New(model.KindInternal, model.StageReport, "", "commit", err)
	}
	if _, err := s.Stage(name, payload); err != nil {
		return "", err
	}
	if err := s.Commit(s.dir, []string{name}); err != nil {
		return "", err
	}
	return sm.Digests[len(sm.Digests)-1].Value, nil
}
