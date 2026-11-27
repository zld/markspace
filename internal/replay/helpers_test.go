package replay_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// statFile stats a work-relative path.
func statFile(dir, name string) (os.FileInfo, error) {
	return os.Stat(filepath.Join(dir, filepath.FromSlash(name)))
}

// checkpointPhases reads the checkpoint file and returns stage phases.
func checkpointPhases(t *testing.T, dir string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "checkpoint.json"))
	if err != nil {
		t.Fatalf("read checkpoint: %v", err)
	}
	var doc struct {
		Stages []struct {
			Name  string `json:"name"`
			Phase string `json:"phase"`
		} `json:"stages"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse checkpoint: %v", err)
	}
	out := map[string]string{}
	for _, st := range doc.Stages {
		out[st.Name] = st.Phase
	}
	return out
}
