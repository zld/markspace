// Package manifest seals the archive checklist. Entries are ordered by a
// stable asset key and carry every digest and stage needed to re-verify an
// archived product without rerunning the pipeline.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"example.com/mediavault/internal/fault"
	"example.com/mediavault/internal/model"
)

// Version is the manifest schema version.
const Version = "mediavault-manifest-v1"

// FileName is the manifest written inside the working directory.
const FileName = "manifest.json"

// Chunk is the manifest projection of one committed block.
type Chunk struct {
	Index  int    `json:"index"`
	Frames int64  `json:"frames"`
	Length int64  `json:"length"`
	Digest string `json:"digest"`
}

// RTPInfo records the timeline facts that justify the normalized RTP asset.
type RTPInfo struct {
	Packets      int   `json:"packets"`
	Wraps        int   `json:"wraps"`
	DriftPPM     int64 `json:"drift_ppm"`
	Corrections  int64 `json:"corrections"`
	TotalSamples int64 `json:"total_samples"`
}

// Entry is one archived asset.
type Entry struct {
	Key                   string   `json:"key"`
	Name                  string   `json:"name"`
	Transport             string   `json:"transport"`
	Stage                 string   `json:"stage"`
	ContentDigest         string   `json:"content_digest"`
	SourceDigest          string   `json:"source_digest"`
	SourceContainerDigest string   `json:"source_container_digest"`
	ArchiveDigest         string   `json:"archive_digest"`
	ArchiveSize           int64    `json:"archive_size"`
	ContainerDigest       string   `json:"container_digest"`
	PolicyVersion         string   `json:"policy_version"`
	SampleRateNum         int64    `json:"sample_rate_num"`
	SampleRateDen         int64    `json:"sample_rate_den"`
	Channels              int      `json:"channels"`
	BitsPerSample         int      `json:"bits_per_sample"`
	ChunkFrames           int64    `json:"chunk_frames"`
	Frames                int64    `json:"frames"`
	Blocks                int      `json:"blocks"`
	Chunks                []Chunk  `json:"chunks"`
	RTP                   *RTPInfo `json:"rtp,omitempty"`
}

// File is the on-disk manifest.
type File struct {
	Version   string  `json:"version"`
	Policy    string  `json:"policy"`
	Digest    string  `json:"digest"`
	UniquePCM int     `json:"unique_pcm"`
	Entries   []Entry `json:"entries"`
}

// NewEntry converts an internal asset and its archive facts into a manifest row.
func NewEntry(a model.Asset, archiveDigest string, archiveSize int64, archiveContainer string) Entry {
	e := Entry{
		Key:                   a.Key,
		Name:                  a.Name,
		Transport:             string(a.Transport),
		Stage:                 string(model.StageArchived),
		ContentDigest:         a.ContentDigest,
		SourceDigest:          a.SourceDigest,
		SourceContainerDigest: a.ContainerDigest,
		ArchiveDigest:         archiveDigest,
		ArchiveSize:           archiveSize,
		ContainerDigest:       archiveContainer,
		PolicyVersion:         a.PolicyVersion,
		SampleRateNum:         a.Track.SampleRate.Num,
		SampleRateDen:         a.Track.SampleRate.Den,
		Channels:              a.Track.Channels,
		BitsPerSample:         a.Track.BitsPerSample,
		ChunkFrames:           a.ChunkFrames,
		Frames:                a.Track.TotalFrames,
		Blocks:                len(a.Chunks),
	}
	for _, c := range a.SortedChunks() {
		e.Chunks = append(e.Chunks, Chunk{Index: c.Index, Frames: c.Frames, Length: c.Length, Digest: c.Digest})
	}
	return e
}

// ChunkRefs converts manifest chunks back into model references.
func (e Entry) ChunkRefs() []model.ChunkRef {
	out := make([]model.ChunkRef, len(e.Chunks))
	for i, c := range e.Chunks {
		out[i] = model.ChunkRef{Index: c.Index, Frames: c.Frames, Length: c.Length, Digest: c.Digest}
	}
	return out
}

// Build sorts entries by key, counts unique WAV PCM digests and seals the
// manifest digest. The result is deterministic for a given entry set.
func Build(policyVersion string, entries []Entry) (*File, error) {
	sorted := append([]Entry(nil), entries...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })
	seen := make(map[string]bool)
	unique := 0
	for _, e := range sorted {
		if e.Transport != string(model.TransportWAV) {
			continue
		}
		if !seen[e.ContentDigest] {
			seen[e.ContentDigest] = true
			unique++
		}
	}
	f := &File{Version: Version, Policy: policyVersion, UniquePCM: unique, Entries: sorted}
	digest, err := computeDigest(f)
	if err != nil {
		return nil, err
	}
	f.Digest = digest
	return f, nil
}

// computeDigest hashes the canonical JSON of the manifest without its own
// digest field, so verification is independent of file formatting.
func computeDigest(f *File) (string, error) {
	type digestView struct {
		Version   string  `json:"version"`
		Policy    string  `json:"policy"`
		UniquePCM int     `json:"unique_pcm"`
		Entries   []Entry `json:"entries"`
	}
	v := digestView{Version: f.Version, Policy: f.Policy, UniquePCM: f.UniquePCM, Entries: f.Entries}
	if v.Entries == nil {
		v.Entries = []Entry{}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fault.Wrap(fault.CodeManifest, fault.StageArchived, -1, err, "encode manifest digest view")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Validate checks structural invariants shared by writer and verifier.
func (f *File) Validate() error {
	if f == nil {
		return fault.New(fault.CodeManifest, fault.StageArchived, -1, "manifest is empty")
	}
	if f.Version != Version {
		return fault.New(fault.CodeManifest, fault.StageArchived, -1, "unsupported manifest version %q", f.Version)
	}
	last := ""
	for i, e := range f.Entries {
		if i > 0 && e.Key <= last {
			return fault.New(fault.CodeManifest, fault.StageArchived, -1, "entries are not strictly ordered at %q", e.Key)
		}
		last = e.Key
		if e.Stage != string(model.StageArchived) {
			return fault.New(fault.CodeManifest, fault.StageArchived, -1, "entry %q is not archived", e.Key)
		}
		if !isDigest(e.ContentDigest) || !isDigest(e.ArchiveDigest) || !isDigest(e.ContainerDigest) ||
			(e.SourceDigest != "" && !isDigest(e.SourceDigest)) {
			return fault.New(fault.CodeManifest, fault.StageArchived, -1, "entry %q has a malformed digest", e.Key)
		}
		for _, c := range e.Chunks {
			if !isDigest(c.Digest) {
				return fault.New(fault.CodeManifest, fault.StageArchived, -1,
					"entry %q block %d has a malformed digest", e.Key, c.Index)
			}
		}
		if len(e.Chunks) != e.Blocks || e.Blocks == 0 {
			return fault.New(fault.CodeManifest, fault.StageArchived, -1,
				"entry %q declares %d blocks but carries %d chunks", e.Key, e.Blocks, len(e.Chunks))
		}
	}
	if !isDigest(f.Digest) {
		return fault.New(fault.CodeManifest, fault.StageArchived, -1, "manifest digest is not 64 hex characters")
	}
	return nil
}

func isDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && !strings.ContainsAny(s, "GHIJKLMNOPQRSTUVWXYZ")
}

// Marshal renders the manifest with stable indentation.
func (f *File) Marshal() ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, fault.Wrap(fault.CodeManifest, fault.StageArchived, -1, err, "encode manifest")
	}
	return append(b, '\n'), nil
}

// Unmarshal decodes and validates a manifest.
func Unmarshal(b []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fault.Wrap(fault.CodeManifest, fault.StageArchived, -1, err, "decode manifest")
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return &f, nil
}
