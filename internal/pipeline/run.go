package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"mediakernel/internal/clock"
	"mediakernel/internal/fixture"
)

// Stage version constants are bumped whenever a stage's artifact format or
// semantics change, forcing stale checkpoints/caches to be rebuilt.
const (
	StageParseVersion    = "parse-v1"
	StageValidateVersion = "validate-v1"
	StageIndexVersion    = "index-v1"
	StageQueryVersion    = "query-v1"
	StageReportVersion   = "report-v1"
)

// ErrCrashRehearsal marks the deterministic checkpoint fault: the run was
// interrupted after its checkpoint was durably written.
var ErrCrashRehearsal = errors.New("scheduled checkpoint fault: stage interruption")

// Fault selects the injected failure mode for a run.
type Fault string

const (
	FaultNone       Fault = "none"
	FaultTruncate   Fault = "truncate"
	FaultCheckpoint Fault = "checkpoint"
)

// Options configures one end-to-end regression run.
type Options struct {
	BaseDir     string
	WAVRelPath  string
	OutputDir   string
	Channels    uint16
	SampleRate  uint32
	Frames      int
	BlockFrames int64
	Fault       Fault
	// CrashAfterStage triggers the checkpoint fault after this stage name.
	CrashAfterStage string
}

// RunResult is the quantified outcome consumed by the report package/CLI.
type RunResult struct {
	Frames          int64
	IndexEntries    int
	LocatingErrors  int
	Resume          bool
	ResumeMillis    int64
	ReusedStages    int
	InputDigest     string
	IndexDigest     string
	OutputSHA256    string
	FirstByteOffset int64
}

// digestFile streams a file through SHA-256 without loading it whole.
func digestFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", clock.NewError("pipeline", clock.CatNotFound,
			"open %q for digest", path).Wrap(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", clock.NewError("pipeline", clock.CatChecksum,
			"digest %q failed", path).Wrap(err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// prepareWAV materializes the fixture WAV, applying the truncation fault.
func prepareWAV(o Options) (string, error) {
	wavOpt := fixture.WAVOptions{
		Channels:      o.Channels,
		SampleRate:    o.SampleRate,
		Frames:        o.Frames,
		BitsPerSample: 16,
	}
	buf, err := fixture.BuildWAV(wavOpt)
	if err != nil {
		return "", err
	}
	relPath := o.WAVRelPath
	if o.Fault == FaultTruncate {
		frameSize := int(o.Channels) * 2
		buf, err = fixture.TruncateTail(buf, frameSize/2+1)
		if err != nil {
			return "", err
		}
		relPath = filepath.Join(filepath.Dir(o.WAVRelPath), "truncated.wav")
	}
	clean, err := fsxEnsure(o.BaseDir, relPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(clean, buf, 0o644); err != nil {
		return "", clock.NewError("pipeline", clock.CatInvalidParam,
			"write fixture failed").Wrap(err)
	}
	return clean, nil
}
