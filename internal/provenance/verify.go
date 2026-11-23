package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

// FailureKind classifies verifier failures so callers can map them to exit
// codes: cache/provenance failures are distinct from input errors.
type FailureKind string

const (
	FailArtifactDigest FailureKind = "artifact_digest_mismatch"
	FailInputDigest    FailureKind = "input_digest_mismatch"
	FailInputSpan      FailureKind = "input_span_mismatch"
	FailConfigDigest   FailureKind = "config_digest_mismatch"
	FailSegmentHash    FailureKind = "segment_hash_mismatch"
	FailCheckpoint     FailureKind = "checkpoint_mismatch"
	FailMissing        FailureKind = "missing_provenance"
)

// Failure is one verification failure with the field that diverged.
type Failure struct {
	Kind    FailureKind `json:"kind"`
	Subject string      `json:"subject"`
	Detail  string      `json:"detail"`
}

func (f Failure) Error() string {
	return fmt.Sprintf("provenance %s (%s): %s", f.Kind, f.Subject, f.Detail)
}

// Expected describes the ground truth the verifier recomputes independently.
type Expected struct {
	ArtifactPath   string
	ArtifactSHA256 string
	ManifestSHA256 string
	ConfigDigest   string
	SegmentHashes  []string
	// InputPaths maps an input path to a reader factory. The verifier hashes
	// each referenced span itself rather than trusting the record.
	InputPaths map[string]func() (io.ReadCloser, error)
}

// Verify re-derives every digest in rec from files and source bytes and returns
// all divergences. A nil/empty result means the record is traceable.
func Verify(rec *Record, exp Expected) []Failure {
	if rec == nil {
		return []Failure{{Kind: FailMissing, Subject: "record", Detail: "no provenance record"}}
	}
	var failures []Failure
	if exp.ManifestSHA256 != "" && rec.ManifestSHA256 != exp.ManifestSHA256 {
		failures = append(failures, Failure{Kind: FailConfigDigest, Subject: "manifest", Detail: "manifest digest differs"})
	}
	if exp.ConfigDigest != "" && rec.ConfigDigest != exp.ConfigDigest {
		failures = append(failures, Failure{Kind: FailConfigDigest, Subject: "config", Detail: "config digest differs"})
	}
	if exp.ArtifactSHA256 != "" && rec.ArtifactSHA256 != exp.ArtifactSHA256 {
		failures = append(failures, Failure{Kind: FailArtifactDigest, Subject: "artifact", Detail: "artifact digest differs"})
	}
	if exp.ArtifactPath != "" {
		sum, err := hashFile(exp.ArtifactPath)
		switch {
		case err != nil:
			failures = append(failures, Failure{Kind: FailMissing, Subject: "artifact", Detail: err.Error()})
		case sum != rec.ArtifactSHA256:
			failures = append(failures, Failure{Kind: FailArtifactDigest, Subject: "artifact", Detail: "artifact file digest differs"})
		}
	}
	failures = append(failures, verifyInputs(rec, exp)...)
	failures = append(failures, verifySegments(rec, exp)...)
	checkpointIDs := map[string]bool{}
	for _, cp := range rec.Checkpoints {
		if cp.ID == "" || len(cp.Digest) != 64 || cp.Offset < 0 {
			failures = append(failures, Failure{Kind: FailCheckpoint, Subject: "checkpoint", Detail: "malformed checkpoint " + cp.ID})
			continue
		}
		if checkpointIDs[cp.ID] {
			failures = append(failures, Failure{Kind: FailCheckpoint, Subject: cp.ID, Detail: "duplicate checkpoint"})
		}
		checkpointIDs[cp.ID] = true
	}
	return failures
}

func verifyInputs(rec *Record, exp Expected) []Failure {
	var failures []Failure
	if len(rec.Inputs) == 0 {
		return []Failure{{Kind: FailMissing, Subject: "inputs", Detail: "record names no input bytes"}}
	}
	for _, in := range rec.Inputs {
		open, ok := exp.InputPaths[in.Path]
		if !ok {
			failures = append(failures, Failure{Kind: FailMissing, Subject: in.Path, Detail: "no reader for input"})
			continue
		}
		if in.Span.Offset < 0 || in.Span.Length < 0 {
			failures = append(failures, Failure{Kind: FailInputSpan, Subject: in.Path, Detail: "negative span"})
			continue
		}
		r, err := open()
		if err != nil {
			failures = append(failures, Failure{Kind: FailMissing, Subject: in.Path, Detail: err.Error()})
			continue
		}
		sum, err := hashSpan(r, in.Span)
		r.Close()
		if err != nil {
			failures = append(failures, Failure{Kind: FailInputSpan, Subject: in.Path, Detail: err.Error()})
			continue
		}
		if sum != in.SHA256 {
			failures = append(failures, Failure{Kind: FailInputSpan, Subject: in.Path, Detail: "span bytes changed"})
		}
	}
	return failures
}

func verifySegments(rec *Record, exp Expected) []Failure {
	if len(exp.SegmentHashes) == 0 {
		return nil
	}
	if len(rec.SegmentHashes) != len(exp.SegmentHashes) {
		return []Failure{{Kind: FailSegmentHash, Subject: "segments", Detail: fmt.Sprintf("%d recorded vs %d recomputed", len(rec.SegmentHashes), len(exp.SegmentHashes))}}
	}
	var failures []Failure
	for i, want := range exp.SegmentHashes {
		if rec.SegmentHashes[i] != want {
			failures = append(failures, Failure{Kind: FailSegmentHash, Subject: fmt.Sprintf("segment[%d]", i), Detail: "hash differs"})
		}
	}
	return failures
}

// HashReader hashes an entire reader.
func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashSpanReader hashes only span bytes from r, which must be seekable.
func HashSpanReader(r io.ReadSeeker, sp Span) (string, error) {
	if _, err := r.Seek(sp.Offset, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.CopyN(h, r, sp.Length)
	if err != nil {
		return "", fmt.Errorf("span [%d,%d): %w", sp.Offset, sp.Offset+sp.Length, err)
	}
	if n != sp.Length {
		return "", errors.New("short read")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashSpan(rc io.ReadCloser, sp Span) (string, error) {
	seeker, ok := rc.(io.ReadSeeker)
	if !ok {
		// Non-seekable readers only support whole-file spans.
		if sp.Offset != 0 {
			return "", errors.New("non-seekable reader requires offset 0")
		}
		h := sha256.New()
		n, err := io.CopyN(h, rc, sp.Length)
		if err != nil || n != sp.Length {
			return "", errors.New("short read")
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	return HashSpanReader(seeker, sp)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return HashReader(f)
}
