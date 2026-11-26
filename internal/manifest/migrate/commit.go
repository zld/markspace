package migrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"mediaprep/internal/manifest"
	"mediaprep/internal/model"
)

// Resume reloads the prepared plan and performs only the atomic commit step.
func Resume(root string, now time.Time) (*Result, error) {
	plan, err := LoadPlan(root)
	if err != nil {
		return nil, err
	}
	return Commit(root, plan, now)
}

// Commit validates v2 once more, preserves the old pointer as a rollback
// point, and atomically renames a temp pointer over current.json.
func Commit(root string, plan *Plan, now time.Time) (*Result, error) {
	p := NewPaths(root)
	v2Bytes, err := os.ReadFile(p.V2)
	if err != nil {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"read prepared v2: %v", err)
	}
	if got := sha256Hex(v2Bytes); plan.V2SHA256 != "" && got != plan.V2SHA256 {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"prepared v2 hash changed: plan=%s disk=%s", plan.V2SHA256, got)
	}
	if _, err := (manifest.V2Adapter{}).Unmarshal(v2Bytes); err != nil {
		return nil, err
	}

	ptr, oldPtrBytes, err := readPointer(p.Pointer)
	if err != nil {
		return nil, err
	}
	oldHash := sha256Hex(oldPtrBytes)
	if ptr.Version == manifest.Version2 {
		return &Result{
			Plan: plan, Pointer: *ptr, PointerSHA256: oldHash,
			PreviousSHA256: plan.PointerSHA256AtPrepare, RollbackPath: p.Rollback,
		}, nil
	}
	if ptr.Version != manifest.Version1 {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"refusing commit from unexpected pointer %s", ptr.Version)
	}

	// The rollback file is an exact copy of the v1 pointer bytes, so its hash
	// and semantics remain independently verifiable.
	if err := writeAtomic(p.Rollback, oldPtrBytes, 0o644); err != nil {
		return nil, err
	}

	newPtr := manifest.Pointer{
		Version: manifest.Version2, File: V2File, SHA256: plan.V2SHA256,
		UpdatedAt: now.UTC().Format(time.RFC3339Nano), RollbackOf: manifest.Version1,
	}
	newBytes, err := json.MarshalIndent(newPtr, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(p.Pointer, newBytes, 0o644); err != nil {
		return nil, err
	}

	plan.Committed = true
	planBytes, err := json.MarshalIndent(plan, "", "  ")
	if err == nil {
		_ = writeAtomic(p.Plan, planBytes, 0o644)
	}

	rbPtr := &manifest.Pointer{}
	if rbBytes, err := os.ReadFile(p.Rollback); err == nil {
		_ = json.Unmarshal(rbBytes, rbPtr)
	}
	return &Result{
		Plan: plan, Pointer: newPtr, PointerSHA256: sha256Hex(newBytes),
		PreviousSHA256: oldHash, RollbackPath: p.Rollback, RollbackPointer: rbPtr,
	}, nil
}

// Rollback atomically restores the preserved v1 pointer. The v2 pointer is
// saved as rollback-v2.json before the switch.
func Rollback(root string, now time.Time) (*Result, error) {
	p := NewPaths(root)
	v1PtrBytes, err := os.ReadFile(p.Rollback)
	if err != nil {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"read v1 rollback point: %v", err)
	}
	var v1Ptr manifest.Pointer
	if err := json.Unmarshal(v1PtrBytes, &v1Ptr); err != nil {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"invalid v1 rollback pointer: %v", err)
	}
	currentBytes, err := os.ReadFile(p.Pointer)
	if err == nil {
		_ = writeAtomic(filepath.Join(p.Dir, "rollback-v2.json"), currentBytes, 0o644)
	}
	if err := writeAtomic(p.Pointer, v1PtrBytes, 0o644); err != nil {
		return nil, err
	}
	return &Result{
		Pointer: v1Ptr, PointerSHA256: sha256Hex(v1PtrBytes), RollbackPath: p.Rollback,
	}, nil
}

func writeAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
