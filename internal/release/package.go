package release

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"assetpack/internal/clock"
)

var ErrPublish = errors.New("publish failed")

// Commit verifies every artifact, then atomically switches the current version.
func Commit(req CommitRequest) (*CommitResult, error) {
	if req.Manifest == nil || req.Root == "" {
		return nil, fmt.Errorf("root and manifest are required")
	}
	if req.Manifest.SchemaVersion == 0 {
		req.Manifest.SchemaVersion = SchemaV1
	}
	if req.Manifest.TimebaseHz == 0 {
		req.Manifest.TimebaseHz = clock.Hz
	}
	version := req.Manifest.Version
	if !safeName(version) {
		return nil, fmt.Errorf("unsafe version %q", version)
	}
	if err := os.MkdirAll(filepath.Join(req.Root, VersionsDir), 0o755); err != nil {
		return nil, err
	}

	staging, err := os.MkdirTemp(req.Root, ".staging-*")
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()

	for name, data := range req.Files {
		rel, err := safeRel(name)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(staging, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return nil, err
		}
	}
	for i := range req.Manifest.Artifacts {
		a := &req.Manifest.Artifacts[i]
		rel, err := safeRel(a.Path)
		if err != nil {
			return nil, err
		}
		got, size, err := hashFile(filepath.Join(staging, rel))
		if err != nil {
			return nil, fmt.Errorf("verify artifact %s: %w", a.Path, err)
		}
		if a.SHA256 != "" && a.SHA256 != got {
			return nil, fmt.Errorf("artifact %s hash mismatch", a.Path)
		}
		a.SHA256, a.Size = got, size
	}
	manifestBytes, err := json.MarshalIndent(req.Manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(staging, ManifestName), manifestBytes, 0o644); err != nil {
		return nil, err
	}
	if _, err := VerifyDir(staging); err != nil {
		return nil, err
	}

	final := filepath.Join(req.Root, VersionsDir, version)
	reused := false
	if _, err := os.Stat(final); err == nil {
		if _, err := VerifyDir(final); err != nil {
			return nil, fmt.Errorf("existing version %s is incomplete: %w", version, err)
		}
		reused = true
	} else if os.IsNotExist(err) {
		if err := os.Rename(staging, final); err != nil {
			return nil, err
		}
		committed = true
	} else {
		return nil, err
	}

	state, err := publishCurrent(req.Root, version)
	if err != nil {
		return nil, err
	}
	if err := ExposeLinks(req.Root, version); err != nil {
		return nil, err
	}
	return &CommitResult{VersionDir: final, Manifest: req.Manifest, State: state, Reused: reused}, nil
}

func publishCurrent(root, version string) (State, error) {
	statePath := filepath.Join(root, StateName)
	state := State{}
	if b, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(b, &state)
	} else if !os.IsNotExist(err) {
		return State{}, err
	}
	if state.Current != "" && state.Current != version {
		state.Previous = state.Current
	}
	state.Current = version
	state.History = appendUnique(state.History, version)
	if err := writeState(root, &state); err != nil {
		return State{}, err
	}
	if err := switchCurrentLink(root, version); err != nil {
		return State{}, err
	}
	return state, nil
}

func appendUnique(xs []string, x string) []string {
	for _, v := range xs {
		if v == x {
			return xs
		}
	}
	return append(xs, x)
}

func safeName(s string) bool {
	return s != "" && !strings.ContainsAny(s, `/\`) && s != "." && s != ".."
}

func safeRel(p string) (string, error) {
	if p == "" || filepath.IsAbs(p) {
		return "", fmt.Errorf("unsafe path %q", p)
	}
	clean := filepath.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes package: %q", p)
	}
	return clean, nil
}

func hashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
