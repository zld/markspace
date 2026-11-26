package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"mediaprep/internal/manifest"
	"mediaprep/internal/model"
)

const (
	ManifestDir = "manifest"
	PointerFile = "current.json"
	PlanFile    = "migration-plan.json"
	V1File      = "v1.json"
	V2File      = "v2.json"
	RollbackV1  = "rollback-v1.json"
)

type Paths struct {
	Root     string
	Dir      string
	Pointer  string
	Plan     string
	V1       string
	V2       string
	Rollback string
}

func NewPaths(root string) Paths {
	dir := filepath.Join(root, ManifestDir)
	return Paths{
		Root: root, Dir: dir,
		Pointer:  filepath.Join(dir, PointerFile),
		Plan:     filepath.Join(dir, PlanFile),
		V1:       filepath.Join(dir, V1File),
		V2:       filepath.Join(dir, V2File),
		Rollback: filepath.Join(dir, RollbackV1),
	}
}

type Plan struct {
	From                   string    `json:"from"`
	To                     string    `json:"to"`
	PreparedAt             time.Time `json:"prepared_at"`
	Committed              bool      `json:"committed"`
	V1File                 string    `json:"v1_file"`
	V2File                 string    `json:"v2_file"`
	V1SHA256               string    `json:"v1_sha256"`
	V2SHA256               string    `json:"v2_sha256"`
	PointerSHA256AtPrepare string    `json:"pointer_sha256_at_prepare"`
	Steps                  []string  `json:"steps"`
}

type Result struct {
	Plan            *Plan             `json:"plan,omitempty"`
	Pointer         manifest.Pointer  `json:"pointer"`
	PointerSHA256   string            `json:"pointer_sha256"`
	PreviousSHA256  string            `json:"previous_sha256,omitempty"`
	RollbackPath    string            `json:"rollback_path,omitempty"`
	RollbackPointer *manifest.Pointer `json:"rollback_pointer,omitempty"`
}

// WriteInitialV1 creates the immutable v1 manifest and its current pointer.
func WriteInitialV1(root string, doc *manifest.Document, now time.Time) (*Result, error) {
	p := NewPaths(root)
	if err := os.MkdirAll(p.Dir, 0o755); err != nil {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseManifest, "", -1, "create manifest dir: %v", err)
	}
	v1, err := manifest.V1Adapter{}.Marshal(doc)
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(p.V1, v1, 0o644); err != nil {
		return nil, err
	}
	sum := sha256Hex(v1)
	ptr := manifest.Pointer{
		Version: manifest.Version1, File: V1File, SHA256: sum, UpdatedAt: now.UTC().Format(time.RFC3339Nano),
	}
	ptrBytes, err := json.MarshalIndent(ptr, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(p.Pointer, ptrBytes, 0o644); err != nil {
		return nil, err
	}
	return &Result{Pointer: ptr, PointerSHA256: sha256Hex(ptrBytes)}, nil
}

// Prepare reads and verifies v1, generates and validates v2, and persists a
// resumable plan. It never modifies current.json.
func Prepare(root, from string, stopAfterPrepare bool, now time.Time) (*Plan, error) {
	p := NewPaths(root)
	ptr, ptrBytes, err := readPointer(p.Pointer)
	if err != nil {
		return nil, err
	}
	if from != "" && ptr.Version != from {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"migration requested from %s but current pointer is %s", from, ptr.Version)
	}
	if ptr.Version != manifest.Version1 {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"prepare expects current v1 pointer, found %s", ptr.Version)
	}
	v1Bytes, err := os.ReadFile(p.V1)
	if err != nil {
		return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseMigrate, "", -1, "read v1: %v", err)
	}
	if ptr.SHA256 != "" && sha256Hex(v1Bytes) != ptr.SHA256 {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"v1 manifest hash does not match pointer")
	}
	doc, version, err := manifest.ReadAny(v1Bytes)
	if err != nil {
		return nil, err
	}
	if version != manifest.Version1 {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"%s does not contain v1", p.V1)
	}

	v2Bytes, err := manifest.V2Adapter{}.Marshal(doc)
	if err != nil {
		return nil, err
	}
	v2Doc, err := manifest.V2Adapter{}.Unmarshal(v2Bytes)
	if err != nil {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"generated v2 failed validation: %v", err)
	}
	if v2Doc.Summary.Verdict != doc.Summary.Verdict ||
		v2Doc.Summary.Segments != doc.Summary.Segments ||
		len(v2Doc.Assets) != len(doc.Assets) {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"generated v2 lost summary/assets during validation")
	}
	if err := writeAtomic(p.V2, v2Bytes, 0o644); err != nil {
		return nil, err
	}

	plan := &Plan{
		From: manifest.Version1, To: manifest.Version2, PreparedAt: now.UTC(),
		V1File: V1File, V2File: V2File, V1SHA256: sha256Hex(v1Bytes), V2SHA256: sha256Hex(v2Bytes),
		PointerSHA256AtPrepare: sha256Hex(ptrBytes),
		Steps: []string{
			"verified current v1 pointer and v1 document hash",
			"adapted v1 wire fields to canonical v2 document",
			"marshaled and re-read v2 successfully",
			"wrote v2.json and resumable migration-plan.json",
			"atomic current.json switch pending",
		},
	}
	planBytes, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return plan, err
	}
	if err := writeAtomic(p.Plan, planBytes, 0o644); err != nil {
		return plan, err
	}
	if stopAfterPrepare {
		return plan, model.NewError(model.CodeMigrationStopped, model.PhaseMigrate, "", -1,
			"injected interruption after prepare; current pointer remains %s", ptr.Version).
			Wrap(model.ErrMigrationStopped)
	}
	_, err = Commit(root, plan, now)
	return plan, err
}

// LoadPlan reads the persisted prepare plan for --resume.
func LoadPlan(root string) (*Plan, error) {
	p := NewPaths(root)
	data, err := os.ReadFile(p.Plan)
	if err != nil {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"no resumable migration plan: %v", err)
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, model.NewError(model.CodeMigrationConflict, model.PhaseMigrate, "", -1,
			"invalid migration plan: %v", err)
	}
	return &plan, nil
}

func readPointer(path string) (*manifest.Pointer, []byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, model.NewError(model.CodeMigrationConflict, model.PhaseManifest, "", -1,
			"read current pointer: %v", err)
	}
	var ptr manifest.Pointer
	if err := json.Unmarshal(data, &ptr); err != nil {
		return nil, data, model.NewError(model.CodeMigrationConflict, model.PhaseManifest, "", -1,
			"invalid current pointer: %v", err)
	}
	return &ptr, data, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
