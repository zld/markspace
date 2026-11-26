package fixture

import (
	"fmt"
	"os"
	"path/filepath"
)

// Spec describes the generated sample tree and expected acceptance values.
type Spec struct {
	Case           string `json:"case"`
	Root           string `json:"root"`
	Format         string `json:"format"`
	Entry          string `json:"entry"`
	Catalog        string `json:"catalog"`
	RTPPath        string `json:"rtp_path"`
	VideoTimescale uint32 `json:"video_timescale"`
	AudioTimescale uint32 `json:"audio_timescale"`
	ExpectedFrames int    `json:"expected_frames"`

	Recovered     int `json:"recovered"`
	Lost          int `json:"lost"`
	Unrecoverable int `json:"unrecoverable"`

	TruncatedFile    string `json:"truncated_file,omitempty"`
	TruncationOffset int64  `json:"truncation_offset,omitempty"`
	OOBReference     string `json:"oob_reference,omitempty"`
	OverflowFile     string `json:"overflow_file,omitempty"`
	OverflowOffset   int64  `json:"overflow_offset,omitempty"`
}

// Build generates all samples in code under root. It never reads external
// fixture files and keeps every generated input below root.
func Build(caseName, root string) (*Spec, error) {
	if root == "" {
		return nil, fmt.Errorf("fixture root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	s := &Spec{Case: caseName, Root: root, VideoTimescale: 90000, AudioTimescale: 48000}
	var err error
	switch caseName {
	case "clean", "recovery":
		err = buildNormal(s)
	case "damaged":
		err = buildDamaged(s)
	case "boundary":
		err = buildBoundary(s)
	default:
		return nil, fmt.Errorf("unknown fixture case %q", caseName)
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

func write(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
