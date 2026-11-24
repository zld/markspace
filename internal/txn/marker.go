package txn

import (
	"encoding/json"
	"os"

	"medpipe/internal/fault"
)

// WriteMarker atomically writes the commit marker beside the output.
func WriteMarker(path string, marker Marker) *fault.Error {
	b, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fault.Wrap(fault.ErrCommit, "txn.marker.encode", 0, "encode marker", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fault.Wrap(fault.ErrCommit, "txn.marker.write", 0, tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fault.Wrap(fault.ErrCommit, "txn.marker.rename", 0, path, err)
	}
	return nil
}

// ReadMarker reads a commit marker.
func ReadMarker(path string) (*Marker, *fault.Error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.Wrap(fault.ErrCommit, "txn.marker.read", 0, path, err)
	}
	var m Marker
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fault.Wrap(fault.ErrCommit, "txn.marker.decode", 0, path, err)
	}
	if m.Version != 1 || m.OutputSHA256 == "" {
		return nil, fault.New(fault.ErrCommit, "txn.marker.invalid", 0, "invalid commit marker")
	}
	return &m, nil
}
