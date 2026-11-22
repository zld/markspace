package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"

	"mediapipeline/internal/media"
)

// OutputRecord describes one durable artifact. Key is its idempotency key;
// ContentSHA256 makes an accidental re-computation with different bytes
// detectable rather than silently accepted.
type OutputRecord struct {
	Key           string `json:"key"`
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	ContentSHA256 string `json:"content_sha256"`
	Bytes         int64  `json:"bytes"`
}

// Decision is one deterministic recovery decision taken during a run.
type Decision struct {
	// Stage names the pipeline phase that decided, e.g. "align" or "rtp".
	Stage string `json:"stage"`
	// Action is the strategy applied, from media.Strategy.
	Action string `json:"action"`
	// Category is the diagnostic that motivated the decision, if any.
	Category string `json:"category,omitempty"`
	// Detail is a short human explanation.
	Detail string `json:"detail"`
	// Count how many items the decision affected.
	Count int `json:"count"`
}

// ProvenanceRecord captures everything needed to explain one output: which
// input bytes and media-time range it came from, what decisions were made
// along the way, and what bytes came out.
type ProvenanceRecord struct {
	Schema       int        `json:"schema"`
	RunID        string     `json:"run_id"`
	Stage        string     `json:"stage"`
	InputSHA256  string     `json:"input_sha256"`
	InputBytes   int64      `json:"input_bytes"`
	TimeRange    TimeRange  `json:"time_range"`
	Decisions    []Decision `json:"decisions"`
	OutputSHA256 string     `json:"output_sha256"`
	OutputBytes  int64      `json:"output_bytes"`
	// OutputKeys lists the idempotency keys produced by this stage.
	OutputKeys []string `json:"output_keys"`
}

// TimeRange is a half-open media-time interval in nanoseconds plus the
// original timescale values it was derived from, so the record is checkable
// against the source container without re-deriving the conversion.
type TimeRange struct {
	StartNanos int64  `json:"start_nanos"`
	EndNanos   int64  `json:"end_nanos"`
	Timescale  uint32 `json:"timescale,omitempty"`
	StartTicks *int64 `json:"start_ticks,omitempty"`
	EndTicks   *int64 `json:"end_ticks,omitempty"`
}

// FromTicks builds a range from original (timescale) tick values, recording
// both the converted nanoseconds and the raw ticks.
func FromTicks(start, end int64, timescale uint32) (TimeRange, error) {
	startNs, err := media.ToNanos(start, timescale)
	if err != nil {
		return TimeRange{}, err
	}
	endNs, err := media.ToNanos(end, timescale)
	if err != nil {
		return TimeRange{}, err
	}
	if endNs <= startNs {
		return TimeRange{}, media.NewError(media.InvalidInput,
			"time range [%d,%d) does not advance", start, end)
	}
	s, e := start, end
	return TimeRange{
		StartNanos: int64(startNs),
		EndNanos:   int64(endNs),
		Timescale:  timescale,
		StartTicks: &s,
		EndTicks:   &e,
	}, nil
}

// Validate checks the range is well formed and, when tick values are present,
// that they convert back to the recorded nanosecond bounds.
func (r TimeRange) Validate() error {
	if r.EndNanos <= r.StartNanos {
		return media.NewError(media.InvalidInput,
			"time range [%d,%d) does not advance", r.StartNanos, r.EndNanos)
	}
	if r.StartTicks == nil || r.EndTicks == nil {
		return nil
	}
	if r.Timescale == 0 {
		return media.NewError(media.ZeroTimescale,
			"time range records ticks but no timescale")
	}
	startNs, err := media.ToNanos(*r.StartTicks, r.Timescale)
	if err != nil {
		return err
	}
	endNs, err := media.ToNanos(*r.EndTicks, r.Timescale)
	if err != nil {
		return err
	}
	if int64(startNs) != r.StartNanos || int64(endNs) != r.EndNanos {
		return media.NewError(media.InvariantViolation,
			"time range ticks [%d,%d)@%d convert to [%d,%d), not [%d,%d)",
			*r.StartTicks, *r.EndTicks, r.Timescale, startNs, endNs, r.StartNanos, r.EndNanos)
	}
	return nil
}

// ProvenanceSchema versions the provenance format.
const ProvenanceSchema = 1

// NewProvenance builds a record, computing the output digest.
func NewProvenance(runID, stage string, inputHash string, inputBytes int64, rng TimeRange, decisions []Decision, output []byte, outputKeys []string) (ProvenanceRecord, error) {
	if runID == "" {
		return ProvenanceRecord{}, media.NewError(media.InvalidInput, "provenance needs a run id")
	}
	sum := sha256.Sum256(output)
	return ProvenanceRecord{
		Schema:       ProvenanceSchema,
		RunID:        runID,
		Stage:        stage,
		InputSHA256:  inputHash,
		InputBytes:   inputBytes,
		TimeRange:    rng,
		Decisions:    decisions,
		OutputSHA256: hex.EncodeToString(sum[:]),
		OutputBytes:  int64(len(output)),
		OutputKeys:   outputKeys,
	}, nil
}

// Hash returns the canonical digest of the record itself, which is what the
// checkpoint stores for replay comparison.
func (p ProvenanceRecord) Hash() (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", media.WrapError(media.InvalidInput, err, "encoding provenance")
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// HashFile digests a file's contents and size.
func HashFile(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", 0, media.WrapError(media.InvalidInput, err, "hashing input")
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// HashBytes digests an in-memory buffer.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Explain renders a decision for the terminal.
func (d Decision) Explain() string {
	cat := d.Category
	if cat == "" {
		cat = "none"
	}
	return fmt.Sprintf("%s/%s[%s] x%d: %s", d.Stage, d.Action, cat, d.Count, d.Detail)
}
