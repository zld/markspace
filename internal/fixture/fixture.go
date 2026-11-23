package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Fault selects which defect the generated archive demonstrates.
type Fault string

const (
	// FaultNone is a clean archive.
	FaultNone Fault = "none"
	// FaultDuplicateReorder duplicates and reorders packets without loss.
	FaultDuplicateReorder Fault = "dup-reorder"
	// FaultBurstLoss drops a burst straddling a segment boundary.
	FaultBurstLoss Fault = "burst-loss"
	// FaultTruncatedBox truncates a media segment mid-box.
	FaultTruncatedBox Fault = "truncated-box"
	// FaultInexactTimebase uses an HLS duration that cannot be expressed in
	// whole plan ticks.
	FaultInexactTimebase Fault = "inexact-timebase"
	// FaultManifestBoundary uses an HLS byte range with no resolvable offset.
	FaultManifestBoundary Fault = "manifest-boundary"
	// FaultKeyframeSkew shifts the low layer's keyframe cadence so layers no
	// longer align.
	FaultKeyframeSkew Fault = "keyframe-skew"
	// FaultDigestConflict rewrites one low-representation segment so its
	// digest no longer matches the recorded manifest.
	FaultDigestConflict Fault = "digest-conflict"
	// FaultDurationSkew regenerates the low layer with a slightly different
	// segment duration so the layers drift beyond one target frame.
	FaultDurationSkew Fault = "duration-skew"
	// FaultRTPCollision delivers one sequence number twice with different
	// payloads, which is a stream-integrity failure rather than mere loss.
	FaultRTPCollision Fault = "rtp-collision"
)

// KnownFaults lists every supported fault name.
func KnownFaults() []Fault {
	return []Fault{
		FaultNone, FaultDuplicateReorder, FaultBurstLoss, FaultTruncatedBox,
		FaultInexactTimebase, FaultManifestBoundary, FaultKeyframeSkew, FaultDigestConflict,
		FaultDurationSkew, FaultRTPCollision,
	}
}

// ParseFault validates a fault name.
func ParseFault(s string) (Fault, error) {
	for _, f := range KnownFaults() {
		if string(f) == s {
			return f, nil
		}
	}
	names := make([]string, 0, len(KnownFaults()))
	for _, f := range KnownFaults() {
		names = append(names, string(f))
	}
	return "", fmt.Errorf("unknown fault %q (known: %v)", s, names)
}

// Defaults describe the shape of the generated archive.
const (
	DefaultSegments     = 12
	DefaultTimebase     = 90000
	DefaultFrameTicks   = 3600 // 25 fps
	DefaultSegmentTicks = DefaultFrameTicks * 100
	DefaultKeyframeGap  = 25 // frames
	DefaultRTPClock     = 90000
	DefaultFramesPerSeg = 100
)

// RepConfig describes one representation.
type RepConfig struct {
	ID            string `json:"id"`
	Bandwidth     int64  `json:"bandwidth"`
	Timescale     int64  `json:"timescale"`
	InitialOffset int64  `json:"initial_offset"`
	KeyframeGap   int    `json:"keyframe_gap"`
}

// Config controls fixture generation.
type Config struct {
	Seed       int64
	Root       string
	Fault      Fault
	Segments   int
	FrameTicks int64
	Keyframe   int
	Reps       []RepConfig
}

func (c *Config) defaults() {
	if c.Segments <= 0 {
		c.Segments = DefaultSegments
	}
	if c.FrameTicks <= 0 {
		c.FrameTicks = DefaultFrameTicks
	}
	if c.Keyframe <= 0 {
		c.Keyframe = DefaultKeyframeGap
	}
	if c.Fault == "" {
		c.Fault = FaultNone
	}
	if len(c.Reps) == 0 {
		c.Reps = []RepConfig{
			{ID: "v-high", Bandwidth: 3000000, Timescale: DefaultTimebase, KeyframeGap: c.Keyframe},
			{ID: "v-low", Bandwidth: 600000, Timescale: DefaultTimebase, KeyframeGap: c.Keyframe},
		}
	}
}

// FramesPerSegment is the number of samples per generated segment.
func (c *Config) FramesPerSegment() int { return DefaultFramesPerSeg }

// Manifest is the generated archive's index, persisted for auditing.
type Manifest struct {
	Seed          int64                    `json:"seed"`
	Fault         Fault                    `json:"fault"`
	Timebase      int64                    `json:"timebase"`
	Segments      int                      `json:"segments"`
	FramesPerSeg  int                      `json:"frames_per_segment"`
	FrameTicks    int64                    `json:"frame_ticks"`
	DashFile      string                   `json:"dash_file"`
	HLSFile       string                   `json:"hls_file"`
	RangeFile     string                   `json:"range_file"`
	RangeMedia    string                   `json:"range_media"`
	Reps          []RepConfig              `json:"reps"`
	SegmentsByRep map[string][]SegmentInfo `json:"segments_by_rep"`
	// Files maps a relative path to its SHA-256.
	Files map[string]string `json:"files"`
}

// SegmentInfo is one generated segment's identity and digest.
type SegmentInfo struct {
	ID       string `json:"id"`
	URI      string `json:"uri"`
	Init     string `json:"init"`
	Capture  string `json:"capture"`
	Start    int64  `json:"start"`
	Duration int64  `json:"duration"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	// DeclaredSHA256 is the digest the archive catalog recorded for this
	// segment when it was first written. It is never updated by fault
	// injection, so a later content change is detectable.
	DeclaredSHA256 string `json:"declared_sha256"`
	CaptureSHA     string `json:"capture_sha256"`
}

// Result reports what was written.
type Result struct {
	Root     string
	Manifest *Manifest
}

// Load reads a previously generated manifest.
func Load(root string) (*Manifest, error) {
	data, err := os.ReadFile(filepath.Join(root, "fixture.json"))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("fixture manifest: %w", err)
	}
	return &m, nil
}

// SegmentInfoFor looks up one segment.
func (m *Manifest) SegmentInfoFor(rep, id string) (SegmentInfo, bool) {
	for _, s := range m.SegmentsByRep[rep] {
		if s.ID == id {
			return s, true
		}
	}
	return SegmentInfo{}, false
}

func writeFile(root, rel string, data []byte) (string, error) {
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func writeJSON(root, rel string, v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return writeFile(root, rel, append(data, '\n'))
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
