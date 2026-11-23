package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ProvenanceRecord maps one output segment back to the exact input byte
// ranges and repair rules that produced it, enabling segment-by-segment
// re-verification after a node restart.
type ProvenanceRecord struct {
	SegmentID     int      `json:"segment_id"`
	SegmentSHA256 string   `json:"segment_sha256"`
	SourceID      string   `json:"source_id"`
	InputSHA256   string   `json:"input_sha256"`
	InputOffsets  []int64  `json:"input_offsets"` // Begin offsets of source events
	OffsetRanges  [][2]int64 `json:"offset_ranges"`
	RuleIDs       []string `json:"rule_ids"`
	EventSeqs     []int    `json:"event_seqs"`
	IndexVersion  int      `json:"index_version"`
}

// ProvenanceLog is the append-only mapping for one pipeline run.
type ProvenanceLog struct {
	RunID   string             `json:"run_id"`
	Records []ProvenanceRecord `json:"records"`
}

// Fault simulates environmental failures for recovery testing.
type Fault struct {
	// FailAfterStage, when set, makes Write fail after that stage completes.
	FailAfterStage string
	// FullAfterWrites makes the next N writes fail with ENOSPC-like error.
	FullAfterWrites int
	writesLeft      int
	armed           bool
}

// ErrDiskFull mirrors an ENOSPC condition without touching the real disk.
var ErrDiskFull = errors.New("journal: simulated disk full")

// FaultyStore wraps Store and injects configured write failures.
type FaultyStore struct {
	Inner *Store
	fault *Fault
}

// NewFaultyStore wraps a store with the given fault plan.
func NewFaultyStore(s *Store, f *Fault) *FaultyStore {
	if f != nil && f.FullAfterWrites > 0 {
		f.writesLeft = f.FullAfterWrites
		f.armed = true
	}
	return &FaultyStore{Inner: s, fault: f}
}

// Write applies the fault policy before delegating to the real store.
func (f *FaultyStore) Write(c *Checkpoint) error {
	if f.fault != nil && f.fault.armed {
		if f.fault.FailAfterStage != "" && c.Stage == f.fault.FailAfterStage {
			return fmt.Errorf("%w: injected after %s", ErrDiskFull, c.Stage)
		}
		if f.fault.FullAfterWrites > 0 {
			f.fault.writesLeft--
			if f.fault.writesLeft <= 0 {
				f.fault.armed = false
				return ErrDiskFull
			}
		}
	}
	return f.Inner.Write(c)
}

// Read delegates to the wrapped store.
func (f *FaultyStore) Read(stage string) (*Checkpoint, error) { return f.Inner.Read(stage) }

// LatestValid delegates to the wrapped store.
func (f *FaultyStore) LatestValid(in, cfg string) (*Checkpoint, int, error) {
	return f.Inner.LatestValid(in, cfg)
}

// Reset delegates to the wrapped store.
func (f *FaultyStore) Reset() error { return f.Inner.Reset() }

// Add appends a provenance record; segment IDs must be unique and ordered.
func (l *ProvenanceLog) Add(r ProvenanceRecord) error {
	for _, ex := range l.Records {
		if ex.SegmentID == r.SegmentID {
			return fmt.Errorf("journal: duplicate segment id %d", r.SegmentID)
		}
	}
	l.Records = append(l.Records, r)
	return nil
}

// Save writes the provenance log atomically.
func (l *ProvenanceLog) Save(path string) error {
	sort.Slice(l.Records, func(i, j int) bool {
		return l.Records[i].SegmentID < l.Records[j].SegmentID
	})
	body, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadProvenance reads a provenance log back for verification.
func LoadProvenance(path string) (*ProvenanceLog, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var l ProvenanceLog
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// SegmentHash computes the stable SHA-256 of a segment's canonical content:
// the ordered content hashes of its source events. Repeated runs over the
// same input produce identical hashes, which the demo relies on.
func SegmentHash(contentHashes []string) string {
	h := sha256.New()
	for _, ch := range contentHashes {
		h.Write([]byte(ch))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Lookup returns the provenance record for a segment, or false when absent.
func (l *ProvenanceLog) Lookup(segmentID int) (ProvenanceRecord, bool) {
	i := sort.Search(len(l.Records), func(i int) bool {
		return l.Records[i].SegmentID >= segmentID
	})
	if i < len(l.Records) && l.Records[i].SegmentID == segmentID {
		return l.Records[i], true
	}
	return ProvenanceRecord{}, false
}

// Verify checks that every record's offsets are present within the supplied
// valid input range and that rule references resolve.
func (l *ProvenanceLog) Verify(inputSize int64, knownRules map[string]bool) []error {
	var errs []error
	for _, r := range l.Records {
		for _, off := range r.InputOffsets {
			if off < 0 || off >= inputSize {
				errs = append(errs, fmt.Errorf(
					"segment %d references out-of-range offset %d", r.SegmentID, off))
			}
		}
		for _, rid := range r.RuleIDs {
			if !knownRules[rid] {
				errs = append(errs, fmt.Errorf(
					"segment %d references unknown rule %s", r.SegmentID, rid))
			}
		}
	}
	return errs
}
