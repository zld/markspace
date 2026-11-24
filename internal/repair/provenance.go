package repair

import "media-archiver/internal/media"

// InputRecord identifies the exact source bytes and run configuration.
type InputRecord struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Seed      int64  `json:"seed,omitempty"`
	Injection string `json:"injection,omitempty"`
	ConfigSHA string `json:"config_sha256"`
}

// BlockProvenance is the stable per-result source and decision record.
type BlockProvenance struct {
	Index               int      `json:"index"`
	ChunkOffset         int64    `json:"chunk_offset"`
	OriginalOffset      int64    `json:"original_offset"`
	FrameCount          int64    `json:"frame_count"`
	StartFrame          int64    `json:"start_frame"`
	EndFrame            int64    `json:"end_frame"`
	TimeRangeNS         [2]int64 `json:"time_range_ns"`
	AudioSHA256         string   `json:"audio_sha256"`
	OriginalTimestampNS *int64   `json:"original_timestamp_ns,omitempty"`
	RepairCodes         []string `json:"repair_codes,omitempty"`
	Bad                 bool     `json:"bad"`
	CorruptOffset       int64    `json:"corrupt_offset,omitempty"`
}

// ProvenanceDocument binds input digest, policy, decisions, and block records.
type ProvenanceDocument struct {
	SchemaVersion int               `json:"schema_version"`
	Input         InputRecord       `json:"input"`
	Policy        Policy            `json:"policy"`
	Decisions     []Decision        `json:"decisions"`
	Blocks        []BlockProvenance `json:"blocks"`
}

func NewProvenance(input InputRecord, policy Policy, result *TimelineResult) *ProvenanceDocument {
	doc := &ProvenanceDocument{
		SchemaVersion: 1,
		Input:         input,
		Policy:        policy,
		Decisions:     []Decision{},
		Blocks:        []BlockProvenance{},
	}
	if result == nil {
		return doc
	}
	doc.Decisions = append(doc.Decisions, result.Decisions...)
	for _, b := range result.Blocks {
		doc.Blocks = append(doc.Blocks, BlockProvenance{
			Index:               b.Index,
			ChunkOffset:         b.ChunkOffset,
			OriginalOffset:      b.OriginalOffset,
			FrameCount:          b.FrameCount,
			StartFrame:          b.StartFrame,
			EndFrame:            b.EndFrame,
			TimeRangeNS:         [2]int64{b.StartNS, b.EndNS},
			AudioSHA256:         b.AudioSHA256,
			OriginalTimestampNS: b.OriginalTimestampNS,
			RepairCodes:         append([]string(nil), b.RepairCodes...),
			Bad:                 b.Bad,
			CorruptOffset:       b.CorruptOffset,
		})
	}
	return doc
}

// CanonicalJSON returns deterministic provenance bytes.
func (p *ProvenanceDocument) CanonicalJSON() ([]byte, error) {
	return media.MarshalCanonicalJSON(p)
}
