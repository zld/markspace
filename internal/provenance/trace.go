// Package provenance records how outputs map back to inputs. A Trace holds
// the input digest, a canonical configuration digest, and an explicit list
// of output artifacts with the frame ranges and stages that produced them,
// so every result can be audited from output back to input frames.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"example.com/continuity/internal/checkpoint"
	"example.com/continuity/internal/fault"
)

// TraceFile is the provenance artifact name inside the run directory.
const TraceFile = "provenance.json"

// Source describes one input stream and its digest.
type Source struct {
	Kind   string `json:"kind"` // "frames" or "rtp"
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}

// OutputSource links one output artifact to its producing stage and frames.
type OutputSource struct {
	Artifact    string `json:"artifact"`
	Stage       string `json:"stage"`
	FrameStart  uint64 `json:"frame_start"`
	FrameEnd    uint64 `json:"frame_end"`
	InputDigest string `json:"input_digest"`
	Digest      string `json:"digest"`
}

// Trace is the full provenance record of one run.
type Trace struct {
	SchemaVersion int            `json:"schema_version"`
	ConfigVersion string         `json:"config_version"`
	InputDigest   string         `json:"input_digest"`
	ConfigDigest  string         `json:"config_digest"`
	Sources       []Source       `json:"sources"`
	Outputs       []OutputSource `json:"outputs"`
	// TraceChecksum covers every field except itself.
	TraceChecksum string `json:"trace_checksum"`
}

// New starts a trace with the run-level digests.
func New(configVersion, inputDigest, configDigest string) *Trace {
	return &Trace{
		SchemaVersion: 1,
		ConfigVersion: configVersion,
		InputDigest:   inputDigest,
		ConfigDigest:  configDigest,
	}
}

// AddSource registers one input stream digest.
func (t *Trace) AddSource(kind, path, digest string, bytes int64) {
	t.Sources = append(t.Sources, Source{Kind: kind, Path: path, Digest: digest, Bytes: bytes})
}

// AddOutput registers one output artifact with its frame-level source.
func (t *Trace) AddOutput(artifact, stage string, frameStart, frameEnd uint64, digest string) {
	t.Outputs = append(t.Outputs, OutputSource{
		Artifact: artifact, Stage: stage,
		FrameStart: frameStart, FrameEnd: frameEnd,
		InputDigest: t.InputDigest, Digest: digest,
	})
}

// ConfigDigestOf computes a deterministic digest of a configuration value
// encoded as JSON.
func ConfigDigestOf(config any) (string, error) {
	data, err := json.Marshal(config)
	if err != nil {
		return "", fault.New(fault.StageProvenance, "config-digest", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (t *Trace) checksum() (string, error) {
	cp := *t
	cp.TraceChecksum = ""
	data, err := json.Marshal(cp)
	if err != nil {
		return "", fault.New(fault.StageProvenance, "checksum", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// Write atomically persists the trace and returns its bytes and digest.
func (t *Trace) Write(dir string) ([]byte, string, error) {
	sum, err := t.checksum()
	if err != nil {
		return nil, "", err
	}
	t.TraceChecksum = sum
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return nil, "", fault.New(fault.StageProvenance, "marshal", err)
	}
	path := filepath.Join(dir, TraceFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, "", fault.New(fault.StageProvenance, "write", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, "", fault.New(fault.StageProvenance, "rename", err)
	}
	return data, checkpoint.DigestOf(data), nil
}

// Read loads a trace for verification.
func Read(dir string) (*Trace, error) {
	data, err := os.ReadFile(filepath.Join(dir, TraceFile))
	if err != nil {
		return nil, fault.New(fault.StageProvenance, "read", err)
	}
	var t Trace
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fault.New(fault.StageProvenance, "parse", err)
	}
	return &t, nil
}

// Verify recomputes the checksum and confirms the input digest is present on
// every output source, proving full output-to-input traceability.
func (t *Trace) Verify() error {
	want, err := t.checksum()
	if err != nil {
		return err
	}
	if want != t.TraceChecksum {
		return fault.New(fault.StageProvenance, "verify", errors.New("trace checksum mismatch"))
	}
	for _, o := range t.Outputs {
		if o.InputDigest != t.InputDigest {
			return fault.New(fault.StageProvenance, "verify",
				errors.New("output missing matching input digest"))
		}
	}
	return nil
}
