package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"mediapipeline/internal/track"
)

// RecoveryCheckpoint is the recovery breakpoint file.
type RecoveryCheckpoint struct {
	Input            string         `json:"input"`
	InputSHA256      string         `json:"input_sha256"`
	PayloadSHA256In  string         `json:"payload_sha256_in"`
	PayloadSHA256    string         `json:"payload_sha256"`
	EventsIn         int            `json:"events_in"`
	EventsOut        int            `json:"events_out"`
	EventOrderSHA256 string         `json:"event_order_sha256"`
	Tracks           []track.Entry  `json:"tracks"`
	Segments         []SegmentState `json:"segments"`
	UpdatedAt        string         `json:"updated_at"`
}

func writeRecoveryCheckpoint(dir string, cp *RecoveryCheckpoint) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cp.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	buf, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "recovery.json.tmp")
	if err := os.WriteFile(tmp, append(buf, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "recovery.json"))
}

func loadRecoveryCheckpoint(dir string) (*RecoveryCheckpoint, bool) {
	buf, err := os.ReadFile(filepath.Join(dir, "recovery.json"))
	if err != nil {
		return nil, false
	}
	var cp RecoveryCheckpoint
	if err := json.Unmarshal(buf, &cp); err != nil {
		return nil, false
	}
	return &cp, true
}
