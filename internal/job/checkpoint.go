package job

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"regress/internal/model"
)

// checkpointFile is the single state file name inside a job directory.
const checkpointFile = "checkpoint.json"

// writeAtomic serializes v as pretty JSON into dir/name via a temp file in
// the same directory, fsyncs it and renames it into place. Same-directory
// rename is atomic on local filesystems, so a crash can leave either the old
// or the new checkpoint but never a torn one.
func writeAtomic(dir, name string, v any) (err error) {
	if err := checkWithin(dir, name); err != nil {
		return err
	}
	final := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, ".ckpt-*")
	if err != nil {
		return model.NewError(model.StageCommit, "checkpoint-temp", dir, "", 0, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err = enc.Encode(v); err != nil {
		_ = tmp.Close()
		return model.NewError(model.StageCommit, "checkpoint-encode", dir, "", 0, err)
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return model.NewError(model.StageCommit, "checkpoint-fsync", dir, "", 0, err)
	}
	if err = tmp.Close(); err != nil {
		return model.NewError(model.StageCommit, "checkpoint-close", dir, "", 0, err)
	}
	if err = os.Rename(tmpName, final); err != nil {
		return model.NewError(model.StageCommit, "checkpoint-rename", dir, "", 0, err)
	}
	// Persist the rename itself so a crash cannot lose the new checkpoint
	// while the old one is already unlinked.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// readCheckpoint loads and decodes the state file. A missing file is reported
// as (nil, nil) so a fresh job start is not an error condition.
func readCheckpoint(dir string) (*State, error) {
	path := filepath.Join(dir, checkpointFile)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, model.NewError(model.StageCommit, "checkpoint-read", dir, "", 0, err)
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, model.NewError(model.StageCommit, "checkpoint-decode", path, "",
			int64(len(raw)),
			fmt.Errorf("%w: %v", model.ErrCheckpoint, err))
	}
	if st.Stage == "" {
		return nil, model.NewError(model.StageCommit, "checkpoint-validate", path, "",
			int64(len(raw)),
			fmt.Errorf("%w: checkpoint has no stage", model.ErrCheckpoint))
	}
	return &st, nil
}
