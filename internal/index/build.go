// Package index builds and queries the versioned, randomly accessible
// keyframe/metadata index. Every entry retains enough provenance to trace a
// query hit back to the input fingerprint, parse offset, repair rule and the
// index version that produced it.
package index

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"medfix/internal/timeline"
)

// IndexVersion is bumped whenever the on-disk layout changes.
const IndexVersion = 3

// magic frames every index file so a truncated or foreign file is rejected.
var magic = [4]byte{'M', 'F', 'I', 'X'}

// Entry is one index point. Payload bytes are never stored; the content
// hash keeps the entry verifiable without retaining media data.
type Entry struct {
	TrackID     string `json:"track_id"`
	Kind        string `json:"kind"`
	Ticks       int64  `json:"ticks"`
	EndTicks    int64  `json:"end_ticks"`
	Keyframe    bool   `json:"keyframe"`
	SourceID    string `json:"source_id"`
	Fingerprint string `json:"fingerprint"`
	OffsetBegin int64  `json:"offset_begin"`
	OffsetEnd   int64  `json:"offset_end"`
	ContentHash string `json:"content_hash"`
	RuleID      string `json:"rule_id"`
	Conflict    bool   `json:"conflict"`
}

// Index is the serialized, versioned random-access structure.
type Index struct {
	Version          int                    `json:"version"`
	Fingerprint      string                 `json:"fingerprint"` // joint fingerprint of all inputs
	ConfigHash       string                 `json:"config_hash"`
	Entries          []Entry                `json:"entries"`
	Rules            []timeline.RepairRule  `json:"rules"`
	NoKeyframeTracks []string               `json:"no_keyframe_tracks"`
	SHA256           string                 `json:"sha256"` // hash of body without this field
}

// Build constructs an index from an aligned timeline. A timeline carrying
// conflicts is refused: no half-valid index is ever published.
func Build(res *timeline.AlignmentResult, fingerprint, configHash string) (*Index, error) {
	if len(res.Conflicts) > 0 {
		return nil, errors.New("index: refusing to build from conflicting timeline")
	}
	idx := &Index{
		Version: IndexVersion, Fingerprint: fingerprint,
		ConfigHash: configHash, Rules: res.Rules,
		NoKeyframeTracks: res.NoKeyframeTracks,
	}
	for _, ev := range res.Events {
		idx.Entries = append(idx.Entries, Entry{
			TrackID: ev.TrackID, Kind: string(ev.Kind),
			Ticks: ev.Ticks, EndTicks: ev.EndTicks, Keyframe: ev.Keyframe,
			SourceID: ev.SourceID, Fingerprint: fingerprint,
			OffsetBegin: ev.Offset.Begin, OffsetEnd: ev.Offset.End,
			ContentHash: ev.ContentHash, RuleID: ev.RuleID, Conflict: ev.Conflict,
		})
	}
	sortEntries(idx.Entries)
	idx.SHA256 = bodyHash(idx)
	return idx, nil
}

// Save writes the index atomically: data goes to a temp file which is fsynced
// and renamed, so a crash never leaves a partially published index.
func (idx *Index) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := encode(idx)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads and fully verifies an index file: magic, version, framing hash
// and embedded body hash must all match or a structured error is returned.
func Load(path string) (*Index, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Decode(raw)
}

// Decode parses in-memory index bytes (used by truncation tests).
func Decode(raw []byte) (*Index, error) {
	if len(raw) < 8+32 {
		return nil, timeline.NewError(timeline.CodeTruncatedIndex, "commit",
			"", int64(len(raw)), "index file shorter than header", nil)
	}
	if !bytes.Equal(raw[:4], magic[:]) {
		return nil, timeline.NewError(timeline.CodeTruncatedIndex, "commit",
			"", 0, "bad index magic", nil)
	}
	ver := binary.BigEndian.Uint32(raw[4:8])
	if int(ver) != IndexVersion {
		return nil, timeline.NewError(timeline.CodeTruncatedIndex, "commit",
			"", 4, "unsupported index version", nil)
	}
	frameHash := hex.EncodeToString(raw[len(raw)-32:])
	body := raw[8 : len(raw)-32]
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != frameHash {
		return nil, timeline.NewError(timeline.CodeTruncatedIndex, "commit",
			"", int64(len(raw)), "frame checksum mismatch (truncated/corrupt)", nil)
	}
	var idx Index
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, timeline.NewError(timeline.CodeTruncatedIndex, "commit",
			"", 8, "index body unmarshal failed", err)
	}
	if idx.Version != IndexVersion {
		return nil, timeline.NewError(timeline.CodeValidation, "commit",
			"", 8, "embedded version mismatch", nil)
	}
	if idx.SHA256 != bodyHash(&idx) {
		return nil, timeline.NewError(timeline.CodeTruncatedIndex, "commit",
			"", 0, "embedded body hash mismatch", nil)
	}
	return &idx, nil
}

func encode(idx *Index) ([]byte, error) {
	idx.SHA256 = bodyHash(idx)
	body, err := json.Marshal(idx)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.Write(magic[:])
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], IndexVersion)
	out.Write(hdr[:])
	out.Write(body)
	sum := sha256.Sum256(body)
	out.Write(sum[:])
	return out.Bytes(), nil
}

// bodyHash hashes every semantic field except SHA256 itself, yielding a
// stable content identity for the index.
func bodyHash(idx *Index) string {
	cp := *idx
	cp.SHA256 = ""
	b, err := json.Marshal(&cp)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func sortEntries(e []Entry) {
	for i := 1; i < len(e); i++ {
		for j := i; j > 0 && entryLess(e[j], e[j-1]); j-- {
			e[j], e[j-1] = e[j-1], e[j]
		}
	}
}

func entryLess(a, b Entry) bool {
	if a.Ticks != b.Ticks {
		return a.Ticks < b.Ticks
	}
	if a.TrackID != b.TrackID {
		return a.TrackID < b.TrackID
	}
	return a.OffsetBegin < b.OffsetBegin
}
