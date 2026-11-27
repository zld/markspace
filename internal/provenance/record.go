// Package provenance records what an output was derived from: input digests,
// the processing version, and the digest of the produced document.
package provenance

import (
	"encoding/json"
	"time"

	"mediaregress/internal/media"
)

// ProcessingVersion identifies the full pipeline behaviour. It combines the
// parser version, the fingerprint algorithm and this recorder's schema so a
// manifest can be replayed against the code that produced it.
const ProcessingVersion = "mediaregress/1"

// InputSummary is one source file.
type InputSummary struct {
	Path          string `json:"path"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
	TrackCount    int    `json:"track_count"`
	RejectedCount int    `json:"rejected_count"`
}

// OutputSummary covers a produced file or document.
type OutputSummary struct {
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size,omitempty"`
}

// StageRecord names one stage and the code version that ran it.
type StageRecord struct {
	Stage   media.Stage `json:"stage"`
	Version string      `json:"version"`
}

// Record is the provenance document attached to every manifest.
type Record struct {
	ProcessingVersion string        `json:"processing_version"`
	Input             InputSummary  `json:"input"`
	Output            OutputSummary `json:"output"`
	Stages            []StageRecord `json:"stages"`
	TrackIDs          []string      `json:"track_ids"`
	RejectedTracks    []string      `json:"rejected_tracks,omitempty"`
	TruncatedTracks   []string      `json:"truncated_tracks,omitempty"`
	Resumed           bool          `json:"resumed"`
	ReusedSegments    int           `json:"reused_segments"`
	ComputedAt        time.Time     `json:"computed_at"`
	// OutputSHA256 is the digest of the manifest without this field. It is
	// filled in by Seal once the document is otherwise final.
	OutputSHA256 string `json:"output_sha256,omitempty"`
	Complete     bool   `json:"complete"`
}

// New builds a record with every field except the sealing digest.
func New(in InputSummary, stages []StageRecord, trackIDs, rejected, truncated []string) *Record {
	return &Record{
		ProcessingVersion: ProcessingVersion,
		Input:             in,
		Stages:            append([]StageRecord(nil), stages...),
		TrackIDs:          append([]string(nil), trackIDs...),
		RejectedTracks:    append([]string(nil), rejected...),
		TruncatedTracks:   append([]string(nil), truncated...),
		ComputedAt:        time.Unix(0, 0).UTC(),
		Complete:          true,
	}
}

// Canonical renders the record as deterministic JSON without the seal, so the
// same logical run always hashes identically. computed_at is zero for the same
// reason: wall-clock time must not leak into a reproducibility digest.
func (r *Record) canonical() ([]byte, error) {
	c := *r
	c.OutputSHA256 = ""
	return json.Marshal(c)
}

// Seal computes and stores the digest of the canonical record. The digest is
// of everything except itself, so it can be verified without ambiguity.
func (r *Record) Seal() (media.Digest, error) {
	b, err := r.canonical()
	if err != nil {
		return "", media.WrapError(media.ClassCorruptInput, media.StageProvenance, "", -1,
			"cannot canonicalise provenance", err)
	}
	d := media.DigestBytes(b)
	r.OutputSHA256 = string(d)
	return d, nil
}

// Verify re-computes the seal and compares it with the stored value.
func (r *Record) Verify() error {
	want := r.OutputSHA256
	if want == "" {
		return media.NewError(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"provenance record is not sealed")
	}
	got, err := r.Seal()
	if err != nil {
		return err
	}
	if string(got) != want {
		return media.Errorf(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"provenance digest %s does not match recorded %s", got, want)
	}
	return nil
}

// SetOutput records the produced document digest and size.
func (r *Record) SetOutput(path, sha string, size int64) {
	r.Output = OutputSummary{Path: path, SHA256: sha, Size: size}
}

// MarkIncomplete turns a partial run into a record that cannot pass verification
// as a complete manifest.
func (r *Record) MarkIncomplete() { r.Complete = false }

// MarshalIndent renders the record for writing to disk.
func (r *Record) MarshalIndent() ([]byte, error) {
	if !r.Complete {
		// An incomplete record is still written, but flagged, so a damaged run
		// leaves a diagnosable artifact rather than a plausible manifest.
		r.Complete = false
	}
	return json.MarshalIndent(r, "", "  ")
}
