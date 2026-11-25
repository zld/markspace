package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"

	auditerrors "media-audit/internal/errors"
	"media-audit/internal/media"
	"media-audit/internal/metadata"
	"media-audit/internal/recover"
	"media-audit/internal/source"
	"media-audit/internal/task"
)

const SchemaVersion = "media-audit/v1"

type Deviation struct {
	Kind       string  `json:"kind"`
	Unit       string  `json:"unit"`
	Expected   float64 `json:"expected,omitempty"`
	Actual     float64 `json:"actual,omitempty"`
	Delta      float64 `json:"delta"`
	MaxAllowed float64 `json:"max_allowed,omitempty"`
	WithinTol  bool    `json:"within_tolerance"`
}

type RecoverySummary struct {
	Stage              string          `json:"stage"`
	ResumeStage        task.Stage      `json:"resume_stage,omitempty"`
	CompletedStage     task.Stage      `json:"completed_stage,omitempty"`
	CorruptCheckpoints []task.Stage    `json:"corrupt_checkpoints,omitempty"`
	Plans              []recover.Plan  `json:"plans,omitempty"`
	Recomputed         []recover.Range `json:"recomputed_ranges,omitempty"`
	Successful         []recover.Range `json:"successful_ranges,omitempty"`
}

type Item struct {
	StableKey       string                `json:"stable_key"`
	OutputItemID    string                `json:"output_item_id"`
	SampleID        string                `json:"sample_id,omitempty"`
	Kind            string                `json:"kind,omitempty"`
	InputPath       string                `json:"input_path"`
	InputPaths      []string              `json:"input_paths,omitempty"`
	Format          media.Format          `json:"format"`
	SHA256          string                `json:"sha256"`
	Fingerprint     *source.Fingerprint   `json:"fingerprint,omitempty"`
	Fingerprints    []source.Fingerprint  `json:"fingerprints,omitempty"`
	Tracks          []media.Track         `json:"tracks,omitempty"`
	Metadata        *metadata.Result      `json:"metadata_resolution,omitempty"`
	InputByteRange  auditerrors.ByteRange `json:"input_byte_range"`
	Errors          []*auditerrors.Error  `json:"errors,omitempty"`
	ErrorCount      int                   `json:"error_count"`
	ConflictCount   int                   `json:"conflict_count"`
	Deviations      []Deviation           `json:"deviations,omitempty"`
	Recovery        *RecoverySummary      `json:"recovery,omitempty"`
	SubmissionCount int                   `json:"submission_count"`
}

type SampleMapping struct {
	SampleID             string `json:"sample_id"`
	Kind                 string `json:"kind"`
	StableKey            string `json:"stable_key"`
	OutputItemID         string `json:"output_item_id"`
	PrimaryPath          string `json:"primary_path"`
	ExpectedDamageOffset int64  `json:"expected_damage_offset,omitempty"`
}

type Summary struct {
	SampleCount     int   `json:"sample_count"`
	ItemCount       int   `json:"item_count"`
	NormalItemCount int   `json:"normal_item_count"`
	ErrorItemCount  int   `json:"error_item_count"`
	ErrorCount      int   `json:"error_count"`
	ConflictCount   int   `json:"conflict_count"`
	RecomputedBytes int64 `json:"recomputed_bytes"`
}

type Manifest struct {
	SchemaVersion  string          `json:"schema_version"`
	Digest         string          `json:"digest_sha256"`
	SortRule       string          `json:"sort_rule"`
	Summary        Summary         `json:"summary"`
	SampleMappings []SampleMapping `json:"sample_mappings"`
	Items          []*Item         `json:"items"`
}

func TaskKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return "TASK-" + hex.EncodeToString(h.Sum(nil))[:16]
}

func OutputID(key string) string {
	sum := sha256.Sum256([]byte("output:" + key))
	return "OUT-" + hex.EncodeToString(sum[:])[:12]
}

// Canonicalize applies deterministic ordering and derived counts/IDs.
func Canonicalize(items []*Item) []*Item {
	for _, it := range items {
		if it.OutputItemID == "" {
			it.OutputItemID = OutputID(it.StableKey)
		}
		it.ErrorCount = len(it.Errors)
		if it.Metadata != nil {
			it.ConflictCount = len(it.Metadata.Conflicts)
		}
		sort.SliceStable(it.Tracks, func(i, j int) bool { return it.Tracks[i].ID < it.Tracks[j].ID })
		sort.SliceStable(it.Errors, func(i, j int) bool {
			if it.Errors[i].Offset != it.Errors[j].Offset {
				return it.Errors[i].Offset < it.Errors[j].Offset
			}
			return it.Errors[i].Code < it.Errors[j].Code
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].StableKey != items[j].StableKey {
			return items[i].StableKey < items[j].StableKey
		}
		if items[i].SampleID != items[j].SampleID {
			return items[i].SampleID < items[j].SampleID
		}
		return items[i].InputPath < items[j].InputPath
	})
	return items
}

func BuildManifest(items []*Item, mappings []SampleMapping) *Manifest {
	items = Canonicalize(items)
	for i := range mappings {
		mappings[i].OutputItemID = OutputID(mappings[i].StableKey)
	}
	sort.SliceStable(mappings, func(i, j int) bool { return mappings[i].SampleID < mappings[j].SampleID })

	m := &Manifest{
		SchemaVersion:  SchemaVersion,
		SortRule:       "stable_key ASC, sample_id ASC, input_path ASC; errors offset ASC, code ASC",
		SampleMappings: mappings,
		Items:          items,
	}
	m.Summary.SampleCount = len(mappings)
	m.Summary.ItemCount = len(items)
	for _, it := range items {
		m.Summary.ErrorCount += it.ErrorCount
		m.Summary.ConflictCount += it.ConflictCount
		if it.ErrorCount == 0 {
			m.Summary.NormalItemCount++
		} else {
			m.Summary.ErrorItemCount++
		}
		if it.Recovery != nil {
			for _, r := range it.Recovery.Recomputed {
				m.Summary.RecomputedBytes += r.Len()
			}
		}
	}
	m.Digest = Digest(m)
	return m
}

// Digest hashes the canonical manifest with the digest field cleared.
func Digest(m *Manifest) string {
	copyManifest := *m
	copyManifest.Digest = ""
	b, _ := json.Marshal(copyManifest)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func WriteJSON(path string, m *Manifest) error {
	m.Digest = Digest(m)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}

// Merger deduplicates concurrent submissions by stable task key.
type Merger struct {
	mu    sync.Mutex
	items map[string]*Item
}

func NewMerger() *Merger {
	return &Merger{items: make(map[string]*Item)}
}

func (m *Merger) Submit(item *Item) (*Item, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.items[item.StableKey]; ok {
		existing.SubmissionCount++
		return existing, true
	}
	if item.SubmissionCount == 0 {
		item.SubmissionCount = 1
	}
	m.items[item.StableKey] = item
	return item, false
}

func (m *Merger) Items() []*Item {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Item, 0, len(m.items))
	for _, it := range m.items {
		out = append(out, it)
	}
	return Canonicalize(out)
}
