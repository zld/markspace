package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// legacyV1 models the historical on-disk layout. It used flat "media"
// coordinates instead of explicit time fields, had no container hash and
// packed faults as string codes rather than structured rows.
type legacyV1 struct {
	Version   int            `json:"version"`
	TrackSSRC uint32         `json:"track_ssrc"`
	ContentID string         `json:"content_id"`
	MetaID    string         `json:"meta_id"`
	Frames    []legacyFrame  `json:"frames"`
	Errors    []legacyError  `json:"errors"`
	Digest    map[string]int `json:"digest"`
}

type legacyFrame struct {
	N          int    `json:"n"`
	Codec      string `json:"codec"`
	Key        bool   `json:"key"`
	SeqStart   uint64 `json:"seq_start"`
	SeqEnd     uint64 `json:"seq_end"`
	ByteStart  int64  `json:"byte_start"`
	ByteLen    int64  `json:"byte_len"`
	MediaStart int64  `json:"media_start"`
	MediaEnd   int64  `json:"media_end"`
}

type legacyError struct {
	Code      string `json:"code"`
	SeqStart  uint64 `json:"seq_start"`
	SeqEnd    uint64 `json:"seq_end"`
	ByteStart int64  `json:"byte_start"`
	ByteLen   int64  `json:"byte_len"`
}

// Adapter reads any supported on-disk version and writes the current one.
type Adapter struct{}

// NewAdapter constructs the default adapter.
func NewAdapter() *Adapter { return &Adapter{} }

// Read parses JSON from r and upgrades it to the current Manifest.
func (Adapter) Read(r io.Reader) (*Manifest, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	switch probe.Version {
	case 1:
		return readV1(raw)
	case SchemaVersion:
		var m Manifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
		}
		if err := m.Validate(); err != nil {
			return nil, err
		}
		return &m, nil
	default:
		return nil, fmt.Errorf("%w: version=%d", ErrUnsupportedVersion, probe.Version)
	}
}

func readV1(raw []byte) (*Manifest, error) {
	var old legacyV1
	if err := json.Unmarshal(raw, &old); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	m := &Manifest{
		Version: SchemaVersion,
		TrackID: old.TrackSSRC,
		Identity: Identity{
			ContentHash:   old.ContentID,
			ContainerHash: "v1-upgrade:" + old.ContentID,
			MetadataHash:  old.MetaID,
		},
		Samples: make([]SampleRow, 0, len(old.Frames)),
		Faults:  make([]FaultRow, 0, len(old.Errors)),
	}
	for _, f := range old.Frames {
		m.Samples = append(m.Samples, SampleRow{
			Index:      f.N,
			Codec:      f.Codec,
			KeyFrame:   f.Key,
			FirstSeq:   f.SeqStart,
			LastSeq:    f.SeqEnd,
			ByteOffset: f.ByteStart,
			ByteLength: f.ByteLen,
			TimeStart:  f.MediaStart,
			TimeEnd:    f.MediaEnd,
			Duration:   f.MediaEnd - f.MediaStart,
		})
	}
	classCounts := map[string]int{}
	for _, e := range old.Errors {
		m.Faults = append(m.Faults, FaultRow{
			Class:      normalizeLegacyCode(e.Code),
			SeqFirst:   e.SeqStart,
			SeqLast:    e.SeqEnd,
			ByteOffset: e.ByteStart,
			ByteLength: e.ByteLen,
		})
		classCounts[normalizeLegacyCode(e.Code)]++
	}
	m.Summary = Summary{
		Version:       SchemaVersion,
		SampleCount:   len(m.Samples),
		KeyFrameCount: countKeys(m.Samples),
		FaultCount:    len(m.Faults),
		FaultsByClass: classCounts,
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return m, nil
}

// Write serializes the manifest in the current version. Indentation is
// fixed so byte-for-byte output is stable across machines and runs.
func (Adapter) Write(w io.Writer, m *Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	out := m
	if m.Version != SchemaVersion {
		cp := *m
		cp.Version = SchemaVersion
		out = &cp
	}
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// normalizeLegacyCode maps v1 string codes onto the v2 fault classes.
func normalizeLegacyCode(code string) string {
	switch code {
	case "GAP", "LOSS":
		return "missing_packet"
	case "TRUNC":
		return "truncated_nal"
	case "BADLEN":
		return "illegal_length"
	case "UNKNOWN":
		return "unknown_nal"
	default:
		return code
	}
}

func countKeys(rows []SampleRow) int {
	n := 0
	for _, r := range rows {
		if r.KeyFrame {
			n++
		}
	}
	return n
}
