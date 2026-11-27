// Package manifest writes the auditable result manifests: segment, silence
// and dedup records plus a summary block. Every record carries the input
// digest, frame range, configuration version, per-stage digest, canonical
// reference and a record checksum, so any output line can be traced back to
// the exact input frames that produced it.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"example.com/continuity/internal/checkpoint"
	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/fingerprint"
	"example.com/continuity/internal/segment"
	"example.com/continuity/internal/silence"
)

// File names written inside the run directory.
const (
	SegmentsFile = "segments.json"
	SilenceFile  = "silence.json"
	DedupFile    = "dedup.json"
	SummaryFile  = "summary.json"
)

// Header identifies the run that produced every record.
type Header struct {
	SchemaVersion int    `json:"schema_version"`
	ConfigVersion string `json:"config_version"`
	InputDigest   string `json:"input_digest"`
}

// SegmentRecord describes one cut segment.
type SegmentRecord struct {
	Header
	Index          int    `json:"index"`
	Track          string `json:"track"`
	FrameStart     uint64 `json:"frame_start"`
	FrameEnd       uint64 `json:"frame_end"`
	StartNS        int64  `json:"start_ns"`
	EndNS          int64  `json:"end_ns"`
	PayloadBytes   int64  `json:"payload_bytes"`
	StageDigest    string `json:"stage_digest"`
	RecordChecksum string `json:"record_checksum"`
}

// SilenceRecord describes one classified audio interval.
type SilenceRecord struct {
	Header
	StartNS        int64  `json:"start_ns"`
	EndNS          int64  `json:"end_ns"`
	Kind           string `json:"kind"`
	StageDigest    string `json:"stage_digest"`
	RecordChecksum string `json:"record_checksum"`
}

// DedupRecord describes one dedup decision.
type DedupRecord struct {
	Header
	SegmentIndex   int    `json:"segment_index"`
	Kind           string `json:"kind"`
	OriginalIndex  int    `json:"original_index"`
	Fingerprint    string `json:"fingerprint"`
	FrameStart     uint64 `json:"frame_start"`
	FrameEnd       uint64 `json:"frame_end"`
	StartNS        int64  `json:"start_ns"`
	EndNS          int64  `json:"end_ns"`
	InputOffset    int64  `json:"input_offset"`
	RecordChecksum string `json:"record_checksum"`
}

// Summary carries the headline metrics, all on the nanosecond base.
type Summary struct {
	Header
	Frames          int     `json:"frames"`
	Segments        int     `json:"segments"`
	SilenceNS       int64   `json:"silence_ns"`
	UnknownNS       int64   `json:"unknown_ns"`
	Duplicates      int     `json:"duplicates"`
	TargetBitrate   float64 `json:"target_bitrate"`
	ActualBitrate   float64 `json:"actual_bitrate"`
	BitrateErrorPct float64 `json:"bitrate_error_pct"`
	PSNRDB          float64 `json:"psnr_db"`
	SSIM            float64 `json:"ssim"`
	ManifestAlignNS int64   `json:"manifest_alignment_ns"`
	SummaryChecksum string  `json:"summary_checksum"`
}

func recordChecksum(v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", fault.New(fault.StageManifest, "checksum", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// BuildSegmentRecords converts cut segments into checksummed records.
func BuildSegmentRecords(h Header, segs []*segment.Segment) ([]SegmentRecord, error) {
	out := make([]SegmentRecord, 0, len(segs))
	for _, seg := range segs {
		rec := SegmentRecord{
			Header: h, Index: seg.Index, Track: seg.Track,
			FrameStart: seg.FrameStart, FrameEnd: seg.FrameEnd,
			StartNS: seg.StartNS, EndNS: seg.EndNS, PayloadBytes: seg.PayloadBytes,
			StageDigest: checkpoint.DigestOf([]byte(fmt.Sprintf(
				"segment:%d:%d:%d:%d:%d", seg.Index, seg.FrameStart, seg.FrameEnd, seg.StartNS, seg.EndNS))),
		}
		sum, err := recordChecksum(rec)
		if err != nil {
			return nil, err
		}
		rec.RecordChecksum = sum
		out = append(out, rec)
	}
	return out, nil
}

// BuildSilenceRecords converts a silence report into checksummed records.
func BuildSilenceRecords(h Header, rep *silence.Report) ([]SilenceRecord, error) {
	out := make([]SilenceRecord, 0, len(rep.Intervals))
	for _, iv := range rep.Intervals {
		kind := "silence"
		if iv.Kind == silence.KindUnknown {
			kind = "unknown"
		}
		rec := SilenceRecord{
			Header: h, StartNS: iv.StartNS, EndNS: iv.EndNS, Kind: kind,
			StageDigest: checkpoint.DigestOf([]byte(fmt.Sprintf(
				"silence:%d:%d:%s", iv.StartNS, iv.EndNS, kind))),
		}
		sum, err := recordChecksum(rec)
		if err != nil {
			return nil, err
		}
		rec.RecordChecksum = sum
		out = append(out, rec)
	}
	return out, nil
}

// BuildDedupRecords converts dedup results into checksummed records.
func BuildDedupRecords(h Header, results []*fingerprint.Result) ([]DedupRecord, error) {
	out := make([]DedupRecord, 0, len(results))
	for _, r := range results {
		kind := "original"
		if r.Kind == fingerprint.KindDuplicate {
			kind = "duplicate"
		}
		rec := DedupRecord{
			Header: h, SegmentIndex: r.SegmentIndex, Kind: kind,
			OriginalIndex: r.OriginalIndex, Fingerprint: hex.EncodeToString(r.Digest[:]),
			FrameStart: r.FrameStart, FrameEnd: r.FrameEnd,
			StartNS: r.StartNS, EndNS: r.EndNS, InputOffset: r.InputOffset,
		}
		sum, err := recordChecksum(rec)
		if err != nil {
			return nil, err
		}
		rec.RecordChecksum = sum
		out = append(out, rec)
	}
	return out, nil
}

func writeJSON(dir, name string, v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", fault.New(fault.StageManifest, "marshal", err)
	}
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fault.New(fault.StageManifest, "write", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return "", fault.New(fault.StageManifest, "rename", err)
	}
	return checkpoint.DigestOf(data), nil
}

// WriteAll atomically writes the three manifests and returns their digests.
func WriteAll(dir string, segRecs []SegmentRecord, silRecs []SilenceRecord,
	dedupRecs []DedupRecord) (map[string]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fault.New(fault.StageManifest, "mkdir", err)
	}
	digests := map[string]string{}
	type item struct {
		name string
		v    any
	}
	for _, it := range []item{
		{SegmentsFile, segRecs}, {SilenceFile, silRecs}, {DedupFile, dedupRecs},
	} {
		d, err := writeJSON(dir, it.name, it.v)
		if err != nil {
			return nil, err
		}
		digests[it.name] = d
	}
	return digests, nil
}

// WriteSummary writes the summary manifest and returns its bytes for the
// checkpoint output digest.
func WriteSummary(dir string, s *Summary) ([]byte, error) {
	// Checksum over the summary excluding its own checksum field.
	cp := *s
	cp.SummaryChecksum = ""
	sum, err := recordChecksum(cp)
	if err != nil {
		return nil, err
	}
	s.SummaryChecksum = sum
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, fault.New(fault.StageManifest, "marshal-summary", err)
	}
	path := filepath.Join(dir, SummaryFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, fault.New(fault.StageManifest, "write-summary", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, fault.New(fault.StageManifest, "rename-summary", err)
	}
	return data, nil
}

// ReadSummary loads a previously written summary for byte-wise verification.
func ReadSummary(dir string) (*Summary, []byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, SummaryFile))
	if err != nil {
		return nil, nil, fault.New(fault.StageManifest, "read-summary", err)
	}
	var s Summary
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, nil, fault.New(fault.StageManifest, "parse-summary", err)
	}
	return &s, data, nil
}
