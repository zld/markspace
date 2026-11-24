package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"mediapipeline/internal/demux"
	"mediapipeline/internal/fault"
)

func fail(sum Summary, err error) Summary {
	if fe, ok := fault.As(err); ok {
		sum.ErrorStage = string(fe.Stage)
		sum.ErrorOp = string(fe.Operation)
		sum.Reason = fe.Reason
		if fe.HasOffset() {
			sum.InputOffset = fe.InputOffset
		}
	} else if err != nil {
		sum.Reason = err.Error()
	}
	return sum
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func writeCheckpoint(dir string, cp *Checkpoint) error {
	cp.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	buf, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "checkpoint.json.tmp")
	if err := os.WriteFile(tmp, append(buf, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "checkpoint.json"))
}

// stageParseRecord is the persisted parse-stage result.
type stageParseRecord struct {
	Format        string        `json:"format"`
	MajorBrand    string        `json:"major_brand"`
	Tracks        int           `json:"tracks"`
	Events        int           `json:"events"`
	Cut           []demux.Range `json:"cut,omitempty"`
	PayloadSHA256 string        `json:"payload_sha256"`
}

func newParseRecord(media *demux.Media, sum Summary) stageParseRecord {
	rec := stageParseRecord{
		Format: media.Format, MajorBrand: media.MajorBrand,
		Tracks: len(media.Tracks), Events: len(media.Events),
		Cut: media.Cut, PayloadSHA256: sum.PayloadSHA256In,
	}
	return rec
}
