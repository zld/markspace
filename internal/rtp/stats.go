package rtpparse

import (
	"encoding/json"
	"os"
)

// Stats is the sidecar summary written beside an RTP sidecar. It records the
// declared stream origin so the sync stage can distinguish a genuinely late
// audio start from a shifted timestamp domain.
type Stats struct {
	BaseRTP   uint32 `json:"base_rtp"`
	ClockRate uint32 `json:"clock_rate"`
	Payload   uint8  `json:"payload_type"`
	SSRC      uint32 `json:"ssrc"`
	Packets   int    `json:"packets"`
}

// ReadStats loads a sidecar summary.
func ReadStats(path string) (Stats, error) {
	var st Stats
	data, err := os.ReadFile(path)
	if err != nil {
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	return st, nil
}

// WriteStats serializes a sidecar summary with a trailing newline.
func WriteStats(path string, st Stats) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
