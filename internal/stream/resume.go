package stream

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
)

// Stage names recorded in the state file.
const (
	StageScanned     = "scanned"     // index verified, streaming not finished
	StageInterrupted = "interrupted" // run cancelled mid-stream, resumable
)

// Resume actions reported in the JSON summary.
const (
	ResumeFresh     = "fresh"     // no usable prior state
	ResumeResumed   = "resumed"   // prior state validated and reused
	ResumeRescanned = "rescanned" // state rejected, restarted from scan
)

// State is the checkpoint written next to the output. A later run with
// --resume validates it against the input digest, the index checksum
// and stage invariants before trusting anything it says.
type State struct {
	InputSHA256   string `json:"input_sha256"`
	IndexChecksum string `json:"index_checksum"`
	Stage         string `json:"stage"`
}

// StatePath returns the conventional state path for an output file.
func StatePath(outputPath string) string {
	return outputPath + ".qcstate"
}

// SaveState writes the state atomically (temp file + rename).
func SaveState(path string, st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadState reads a state file; a missing file is not an error.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, nil // unparseable state: treat as absent
	}
	return &st, nil
}

// Validate checks the state against the current input digest and index
// checksum plus stage invariants. ok=false means the caller must
// discard the state and restart from the scanning stage.
func (st *State) Validate(inputSHA [32]byte, indexChecksum string) (ok bool) {
	if st == nil {
		return false
	}
	if st.InputSHA256 != hex.EncodeToString(inputSHA[:]) {
		return false
	}
	if st.IndexChecksum != indexChecksum {
		return false
	}
	switch st.Stage {
	case StageScanned, StageInterrupted:
		return true
	}
	return false
}

// DecideResume picks the resume action for a run.
func DecideResume(resumeRequested bool, st *State, inputSHA [32]byte, indexChecksum string) string {
	if !resumeRequested || st == nil {
		return ResumeFresh
	}
	if st.Validate(inputSHA, indexChecksum) {
		return ResumeResumed
	}
	return ResumeRescanned
}
