// Package merge turns completed stage artifacts into a stable manifest and a
// single binary artifact. The merge key is built from the input digest, track
// number and stage version, and every collection has a fixed sort order, so
// identical inputs always produce byte-identical outputs even when the run
// was interrupted and resumed.
package merge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"rtptrace/internal/fault"
	"rtptrace/internal/stage"
)

// ManifestVersion identifies the manifest schema; bump on incompatible change.
const ManifestVersion = 1

// Entry is one merge row keyed by a stable, content-derived identity.
type Entry struct {
	Key          string `json:"key"`
	InputDigest  string `json:"input_digest"`
	TrackIndex   int    `json:"track_index"`
	StageVersion int    `json:"stage_version"`
	SSRC         uint32 `json:"ssrc"`
	Sequence     uint16 `json:"sequence"`
	RTPTimestamp uint32 `json:"rtp_timestamp"`
	MediaDTS     uint64 `json:"media_dts"`
	Offset       uint64 `json:"sample_offset"`
	Size         uint64 `json:"sample_size"`
	PayloadHash  string `json:"payload_sha256"`
}

// Stats carries the weak-network counters surfaced by the CLI.
type Stats struct {
	Received         int `json:"received"`
	DuplicatePackets int `json:"duplicate_packets"`
	LostPackets      int `json:"lost_packets"`
	ReorderedPackets int `json:"reordered_packets"`
	EmittedPackets   int `json:"emitted_packets"`
}

// Manifest is the fully deterministic top-level document.
type Manifest struct {
	ManifestVersion int     `json:"manifest_version"`
	StageVersion    int     `json:"stage_version"`
	InputDigest     string  `json:"input_digest"`
	Seed            int64   `json:"seed"`
	Stats           Stats   `json:"stats"`
	Entries         []Entry `json:"entries"`
	ArtifactSHA256  string  `json:"artifact_sha256"`
}

// PacketRow is the per-packet input the merger consumes, already in canonical
// form by the RTP stage.
type PacketRow struct {
	TrackIndex   int
	SSRC         uint32
	Sequence     uint16
	RTPTimestamp uint32
	MediaDTS     uint64
	Offset       uint64
	Size         uint64
	Payload      []byte
}

// StableKey composes the required identity: input digest + track + version.
func StableKey(inputDigest string, trackIndex, stageVersion int, seq uint16) string {
	h := sha256.New()
	h.Write([]byte(inputDigest))
	writeByte(h, byte(trackIndex>>24), byte(trackIndex>>16), byte(trackIndex>>8), byte(trackIndex))
	writeByte(h, byte(stageVersion>>24), byte(stageVersion>>16), byte(stageVersion>>8), byte(stageVersion))
	writeByte(h, byte(seq>>8), byte(seq))
	return hex.EncodeToString(h.Sum(nil))
}

func writeByte(h interface{ Write([]byte) (int, error) }, b ...byte) {
	_, _ = h.Write(b)
}

// Build assembles the manifest and artifact from canonical packet rows. The
// artifact is a fixed-layout concatenation: for each row, a 32-byte SHA-256 of
// its payload followed by the payload itself. Rows are sorted by
// (track, sequence) so output never depends on map iteration order.
func Build(inputDigest string, seed int64, rows []PacketRow, stats Stats) (*Manifest, []byte, error) {
	sorted := make([]PacketRow, len(rows))
	copy(sorted, rows)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].TrackIndex != sorted[j].TrackIndex {
			return sorted[i].TrackIndex < sorted[j].TrackIndex
		}
		return sorted[i].Sequence < sorted[j].Sequence
	})

	entries := make([]Entry, 0, len(sorted))
	var artifact []byte
	for _, r := range sorted {
		sum := sha256.Sum256(r.Payload)
		payloadHash := hex.EncodeToString(sum[:])
		entries = append(entries, Entry{
			Key:          StableKey(inputDigest, r.TrackIndex, stage.StageVersion, r.Sequence),
			InputDigest:  inputDigest,
			TrackIndex:   r.TrackIndex,
			StageVersion: stage.StageVersion,
			SSRC:         r.SSRC,
			Sequence:     r.Sequence,
			RTPTimestamp: r.RTPTimestamp,
			MediaDTS:     r.MediaDTS,
			Offset:       r.Offset,
			Size:         r.Size,
			PayloadHash:  payloadHash,
		})
		artifact = append(artifact, sum[:]...)
		artifact = append(artifact, r.Payload...)
	}
	artSum := sha256.Sum256(artifact)

	m := &Manifest{
		ManifestVersion: ManifestVersion,
		StageVersion:    stage.StageVersion,
		InputDigest:     inputDigest,
		Seed:            seed,
		Stats:           stats,
		Entries:         entries,
		ArtifactSHA256:  hex.EncodeToString(artSum[:]),
	}
	return m, artifact, nil
}

// EncodeManifest renders JSON with a trailing newline and stable key order
// (struct field order), guaranteeing byte-identical output for equal values.
func EncodeManifest(m *Manifest) ([]byte, error) {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fault.Newf(fault.KindInternal, "manifest", fault.NoOffset, "marshal: %v", err)
	}
	return append(data, '\n'), nil
}
