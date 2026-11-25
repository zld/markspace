package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// generateFixture writes the full sample fixture into out: a normal media
// set plus damaged and conflicting variants, and the event records.
func generateFixture(out string) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found in PATH: %w", err)
	}
	if err := ensureDir(out); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "replay-gen-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	normal := join(out, "normal")
	if err := renderSet(join(tmp, "normal.mp4"), normal, defaultMaster()); err != nil {
		return err
	}

	// Damaged variants derived from the normal set.
	if err := makeGapSet(normal, join(out, "gap")); err != nil {
		return err
	}
	if err := makeTfdtRegressSet(normal, join(out, "tfdt-regress")); err != nil {
		return err
	}
	if err := makeDupSet(normal, join(out, "dup")); err != nil {
		return err
	}
	if err := makeTruncatedSet(normal, join(out, "truncated")); err != nil {
		return err
	}

	// Media-policy variants re-rendered with different parameters.
	af := defaultMaster()
	af.audioRate = 44100
	if err := renderSet(join(tmp, "af.mp4"), join(out, "audio-conflict"), af); err != nil {
		return err
	}
	au := defaultMaster()
	au.audioCh = 6
	if err := renderSet(join(tmp, "au.mp4"), join(out, "audio-unsupported"), au); err != nil {
		return err
	}
	cm := defaultMaster()
	cm.writeColr = false
	cm.colorTags = nil // untagged frames: no colr box is written
	if err := renderSet(join(tmp, "cm.mp4"), join(out, "color-missing"), cm); err != nil {
		return err
	}
	cc := defaultMaster()
	cc.colorTags = []string{"bt2020", "bt2020-10", "bt2020nc"}
	if err := renderSet(join(tmp, "cc.mp4"), join(out, "color-conflict"), cc); err != nil {
		return err
	}

	evs := map[string]any{"events": []map[string]any{
		{"id": "kill-1", "type": "kill", "time_ms": 12000},
		{"id": "kill-2", "type": "kill", "time_ms": 19800},
		{"id": "kill-3", "type": "kill", "time_ms": 21000},
	}}
	b, err := json.MarshalIndent(evs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(join(out, "events.json"), b, 0o644)
}

// renderSet renders a master and splits it into a CMAF set directory.
func renderSet(master, setDir string, o masterOpts) error {
	if err := renderMaster(master, o); err != nil {
		return err
	}
	n, err := splitMaster(master, setDir)
	if err != nil {
		return err
	}
	fmt.Printf("  set %s: %d fragments\n", filepath.Base(setDir), n)
	return nil
}

// copySet duplicates a set directory (flat, files only).
func copySet(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := ensureDir(dst); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(join(dst, e.Name()), join(src, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
