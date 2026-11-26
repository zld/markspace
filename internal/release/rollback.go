package release

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"assetpack/internal/render"
)

var (
	ErrNoPrevious     = errors.New("no previous complete release")
	ErrRollbackFailed = errors.New("rollback failed")
)

// VerifyWithPixels recomputes RGBA means and PCM peak from packaged bytes.
func VerifyWithPixels(dir string) (*Manifest, error) {
	m, err := VerifyDir(dir)
	if err != nil {
		return nil, err
	}
	for _, target := range m.Targets {
		var sum render.RGBMean
		count := 0
		var audio []int16
		for _, a := range m.Artifacts {
			if a.Kind != "audio" && a.Platform != target.Platform {
				continue
			}
			full := filepath.Join(dir, a.Path)
			switch a.Kind {
			case "frame":
				b, err := os.ReadFile(full)
				if err != nil {
					return m, err
				}
				mean, err := render.ComputeRGBMean(b)
				if err != nil {
					return m, fmt.Errorf("%w: %s: %v", ErrVerification, a.Path, err)
				}
				sum.R += mean.R
				sum.G += mean.G
				sum.B += mean.B
				count++
			case "audio":
				b, err := os.ReadFile(full)
				if err != nil {
					return m, err
				}
				for len(b) >= 2 {
					audio = append(audio, int16(binary.LittleEndian.Uint16(b)))
					b = b[2:]
				}
			}
		}
		if count != target.FrameCount {
			return m, fmt.Errorf("%w: %s verified %d frames, manifest says %d", ErrVerification, target.Platform, count, target.FrameCount)
		}
		if count > 0 {
			got := RGB{R: sum.R / float64(count), G: sum.G / float64(count), B: sum.B / float64(count)}
			if math.Abs(got.R-target.RGBMean.R) > 0.01 || math.Abs(got.G-target.RGBMean.G) > 0.01 || math.Abs(got.B-target.RGBMean.B) > 0.01 {
				return m, fmt.Errorf("%w: %s RGB got %+v want %+v", ErrVerification, target.Platform, got, target.RGBMean)
			}
		}
		if len(audio) > 0 {
			peak := render.PeakDBFS(audio)
			if math.Abs(peak-target.PeakDBFS) > 0.01 {
				return m, fmt.Errorf("%w: audio peak %.4f != manifest %.4f", ErrVerification, peak, target.PeakDBFS)
			}
		}
	}
	return m, nil
}

// Rollback verifies the target and atomically points current at it.
func Rollback(ctx context.Context, root, to string, verifyPixels bool) (*Manifest, *State, error) {
	st, err := LoadState(root)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: load state: %v", ErrRollbackFailed, err)
	}
	version := to
	if to == "previous" {
		version = st.Previous
	}
	if version == "" {
		return nil, st, fmt.Errorf("%w: state has no previous version", ErrNoPrevious)
	}
	if !safeName(version) {
		return nil, st, fmt.Errorf("%w: unsafe version %q", ErrRollbackFailed, version)
	}
	dir := filepath.Join(root, VersionsDir, version)
	var m *Manifest
	if verifyPixels {
		m, err = VerifyWithPixels(dir)
	} else {
		m, err = VerifyDir(dir)
	}
	if err != nil {
		return m, st, fmt.Errorf("%w: %v", ErrRollbackFailed, err)
	}
	if err := ctx.Err(); err != nil {
		return m, st, err
	}

	st.Current = version
	if to == "previous" {
		// Keep repeated `--to previous` idempotently on the restored version.
		st.Previous = version
	}
	if err := writeState(root, st); err != nil {
		return m, st, fmt.Errorf("%w: %v", ErrRollbackFailed, err)
	}
	if err := switchCurrentLink(root, version); err != nil {
		return m, st, fmt.Errorf("%w: %v", ErrRollbackFailed, err)
	}
	if err := ExposeLinks(root, version); err != nil {
		return m, st, fmt.Errorf("%w: %v", ErrRollbackFailed, err)
	}
	return m, st, nil
}

func writeState(root string, st *State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(root, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, filepath.Join(root, StateName)); err != nil {
		return err
	}
	return nil
}

func switchCurrentLink(root, version string) error {
	link := filepath.Join(root, CurrentLink)
	tmp := filepath.Join(root, fmt.Sprintf(".current-%d", os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(filepath.Join(VersionsDir, version), tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
