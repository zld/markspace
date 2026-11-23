package pipeline

import (
	"encoding/json"
	"os"

	"github.com/zld/media-check/media"
)

// writeJSONAtomic writes via a temp file in the same directory and renames it.
// A partially written temp file is removed on failure.
func writeJSONAtomic(dir, path string, v interface{}) error {
	if _, err := media.ValidateOutPath(dir, path); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return media.WrapError(media.ClassIntegrity, "json.marshal", 0, err, "marshal %T", v)
	}
	tmp, err := os.CreateTemp(dir, ".json-*.tmp")
	if err != nil {
		return media.WrapError(media.ClassIO, "json.tmp", 0, err, "create temp")
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return media.WrapError(media.ClassIO, "json.write", 0, err, "write temp")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return media.WrapError(media.ClassIO, "json.sync", 0, err, "sync temp")
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return media.WrapError(media.ClassIO, "json.close", 0, err, "close temp")
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return media.WrapError(media.ClassIO, "json.rename", 0, err, "atomic rename")
	}
	return nil
}
