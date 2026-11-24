package manifest

import "encoding/json"

// encodeMetadata renders the provenance record as compact JSON.
func encodeMetadata(m Metadata) []byte {
	b, err := json.Marshal(struct {
		JobID      string  `json:"job_id"`
		SourceRate int     `json:"source_rate"`
		TargetRate int     `json:"target_rate"`
		Layout     string  `json:"layout"`
		TargetLUFS float64 `json:"target_lufs"`
		Packets    uint64  `json:"packets"`
		Gaps       int     `json:"gaps"`
		SamplesIn  int64   `json:"samples_in"`
		SamplesOut int64   `json:"samples_out"`
		SHA256     string  `json:"sha256"`
	}{
		JobID: m.JobID, SourceRate: m.SourceRate, TargetRate: m.TargetRate,
		Layout: m.Layout, TargetLUFS: m.TargetLUFS, Packets: m.Packets,
		Gaps: m.Gaps, SamplesIn: m.SamplesIn, SamplesOut: m.SamplesOut,
		SHA256: m.SHA256,
	})
	if err != nil {
		panic(err) // pure struct marshalling cannot realistically fail
	}
	return b
}
