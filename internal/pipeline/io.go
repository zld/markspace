package pipeline

import (
	"encoding/json"
	"os"
)

// Small indirections keep os/encoding access in one place and the rest of
// the pipeline testable without touching the file system directly.

func readFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func unmarshal(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

func marshalResult(cr CaseResult) ([]byte, error) {
	return json.MarshalIndent(cr, "", "  ")
}

// MarshalReport renders the top-level report deterministically.
func MarshalReport(r *Report) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
