package migrate

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"continuity/internal/media"
)

// Report records the completed migration stages.
type Report struct {
	Path        string `json:"path"`
	Checkpoint  string `json:"checkpoint"`
	Shadow      string `json:"shadow"`
	AlreadyV2   bool   `json:"already_v2"`
	RolledBack  bool   `json:"rolled_back"`
	Checkpoints int    `json:"checkpoints"`
}

// Options allows tests to inject a post-switch verification failure.
type Options struct {
	PostSwitchVerify func(*ManifestV2) error
}

// MigrateFile runs read/validate, checkpoint, shadow write, validation and
// atomic switch. A post-switch verification failure restores the v1 bytes.
func MigrateFile(path string, opts Options) (*Report, error) {
	v2, oldBytes, err := LoadVersioned(path)
	if err != nil {
		return nil, err
	}
	rep := &Report{Path: path, Checkpoint: CheckpointPath(path), Shadow: ShadowPath(path)}
	if v2.Version == 2 && string(oldBytes) != "" {
		var head struct {
			Schema string `json:"schema"`
		}
		_ = json.Unmarshal(oldBytes, &head)
		if head.Schema == SchemaV2 {
			rep.AlreadyV2 = true
		}
	}

	if err := preserveCheckpoint(rep.Checkpoint, oldBytes); err != nil {
		return nil, err
	}
	if !rep.AlreadyV2 {
		data, err := json.MarshalIndent(v2, "", "  ")
		if err != nil {
			return nil, err
		}
		if err := atomicWrite(rep.Shadow, data, 0o644); err != nil {
			return nil, err
		}
		if _, _, err := LoadVersioned(rep.Shadow); err != nil {
			return nil, err
		}
		if err := os.Rename(rep.Shadow, path); err != nil {
			return nil, &media.Fault{Phase: media.PhaseMigrate, Code: media.CodePublication, Source: path, Err: err}
		}
	}

	if opts.PostSwitchVerify != nil {
		current, _, lerr := LoadVersioned(path)
		if lerr != nil {
			rep.RolledBack = true
			if rerr := restoreCheckpoint(path, rep.Checkpoint); rerr != nil {
				return rep, errors.Join(lerr, rerr)
			}
			return rep, lerr
		}
		if verr := opts.PostSwitchVerify(current); verr != nil {
			rep.RolledBack = true
			if rerr := restoreCheckpoint(path, rep.Checkpoint); rerr != nil {
				return rep, errors.Join(verr, rerr)
			}
			return rep, &media.Fault{Phase: media.PhaseMigrate, Code: media.CodeMigrationFailed, Source: path, Err: verr}
		}
	}
	root := filepath.Dir(path)
	rep.Checkpoints, _ = CountCheckpoints(root)
	return rep, nil
}

func preserveCheckpoint(checkpoint string, old []byte) error {
	if _, err := os.Stat(checkpoint); err == nil {
		return nil // never overwrite the pre-switch checkpoint
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWrite(checkpoint, old, 0o444)
}

func restoreCheckpoint(path, checkpoint string) error {
	old, err := os.ReadFile(checkpoint)
	if err != nil {
		return err
	}
	return atomicWrite(path, old, 0o644)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
