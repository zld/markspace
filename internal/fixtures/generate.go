// Package fixtures generates deterministic encoding-metadata fixtures: one
// valid three-rendition ladder and three negative samples (presentation
// offset, missing segment number, duration drift). It also optionally creates
// placeholder segment files so reference-existence checks can be exercised
// fully offline.
package testdata

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"dashforge/internal/model"
)

// Spec names the supported fixtures.
const (
	FixtureValid         = "valid"
	FixtureOffset        = "negative-offset"
	FixtureMissingNumber = "negative-missing-number"
	FixtureDurationDrift = "negative-duration-drift"
)

// Fixtures maps every fixture name to its generator output filename.
var Fixtures = map[string]string{
	FixtureValid:         "valid.json",
	FixtureOffset:        "negative-offset.json",
	FixtureMissingNumber: "negative-missing-number.json",
	FixtureDurationDrift: "negative-duration-drift.json",
}

const (
	segDurMs   int64 = 4000
	segPerRung       = 12
)

type rung struct {
	id        string
	width     int
	height    int
	bandwidth int
	codec     string
	fps       string
}

var ladder = []rung{
	{"v-360p", 640, 360, 800_000, "avc1.64001e", "30/1"},
	{"v-720p", 1280, 720, 2_500_000, "avc1.640028", "30/1"},
	{"v-1080p", 1920, 1080, 5_000_000, "hvc1.1.6.L120", "24/1"},
}

// Valid builds the healthy three-rendition metadata: 36 fragments total,
// uniform 4s boundaries shared by every rung, short final fragment.
func Valid() *model.Metadata {
	meta := &model.Metadata{Title: "dashforge-demo"}
	for _, r := range ladder {
		rd := model.Rendition{
			ID: r.id, Width: r.width, Height: r.height, Bandwidth: r.bandwidth,
			Codec: r.codec, FrameRate: r.fps, StartOffsetMs: 0,
			SegmentDurationMs: segDurMs,
			// Template paths are relative to SegmentRoot; the root owns the
			// "media/" directory prefix so joining never doubles it.
			Initialization: fmt.Sprintf("%s/init.mp4", r.id),
			Media:          fmt.Sprintf("%s/seg-$Number%%05d$.m4s", r.id),
			SegmentRoot:    "media",
		}
		for n := 1; n <= segPerRung; n++ {
			dur := segDurMs
			if n == segPerRung {
				dur = 3960 // final fragment lands one frame early at ~24/30fps
			}
			rd.Segments = append(rd.Segments, model.Segment{
				Number: n, StartMs: int64(n-1) * segDurMs, DurationMs: dur,
			})
		}
		meta.Renditions = append(meta.Renditions, rd)
	}
	return meta
}

// OffsetShifts the 720p rung by 33ms — beyond a 20ms tolerance but otherwise
// intact, exercising the boundary/alignment reporting path.
func Offset() *model.Metadata {
	meta := Valid()
	rd := findRung(meta, "v-720p")
	rd.StartOffsetMs = 33
	return meta
}

// MissingNumber removes segment 7 from the 360p rung, producing a number gap
// and a timeline hole without any silent backfill.
func MissingNumber() *model.Metadata {
	meta := Valid()
	rd := findRung(meta, "v-360p")
	kept := rd.Segments[:0]
	for _, s := range rd.Segments {
		if s.Number != 7 {
			kept = append(kept, s)
		}
	}
	rd.Segments = kept
	return meta
}

// DurationDrift extends the 1080p rung by two extra fragments, pushing its
// total duration ~8s beyond the other rungs.
func DurationDrift() *model.Metadata {
	meta := Valid()
	rd := findRung(meta, "v-1080p")
	nextStart := int64(segPerRung) * segDurMs
	for n := segPerRung + 1; n <= segPerRung+2; n++ {
		rd.Segments = append(rd.Segments, model.Segment{
			Number: n, StartMs: nextStart, DurationMs: segDurMs,
		})
		nextStart += segDurMs
	}
	return meta
}

func findRung(meta *model.Metadata, id string) *model.Rendition {
	for i := range meta.Renditions {
		if meta.Renditions[i].ID == id {
			return &meta.Renditions[i]
		}
	}
	panic("fixture rung " + id + " not found")
}

// All returns every fixture keyed by the canonical filenames.
func All() map[string]*model.Metadata {
	return map[string]*model.Metadata{
		FixtureValid:         Valid(),
		FixtureOffset:        Offset(),
		FixtureMissingNumber: MissingNumber(),
		FixtureDurationDrift: DurationDrift(),
	}
}

// WriteAll serializes every fixture into outDir with stable field ordering
// (encoding/json renders struct fields in declaration order). When
// createFiles is true, placeholder segment files are created under
// outDir/media so on-disk reference checks succeed for the valid fixture.
func WriteAll(outDir string, createFiles bool) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create fixture dir: %w", err)
	}
	for name, meta := range All() {
		path := filepath.Join(outDir, Fixtures[name])
		if err := writeJSON(path, meta); err != nil {
			return err
		}
	}
	if createFiles {
		return writeSegmentPlaceholders(outDir, Valid())
	}
	return nil
}

func writeJSON(path string, meta *model.Metadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("finalize %s: %w", path, err)
	}
	return nil
}

// writeSegmentPlaceholders creates one zero-byte file per expanded media
// reference plus every initialization segment, anchored at outDir. References
// are relative to the rendition's SegmentRoot.
func writeSegmentPlaceholders(outDir string, meta *model.Metadata) error {
	for _, rd := range meta.Renditions {
		if rd.Initialization != "" {
			if err := touch(filepath.Join(outDir, rd.SegmentRoot, rd.Initialization)); err != nil {
				return err
			}
		}
		for _, seg := range rd.Segments {
			rel := expandSimple(rd.Media, seg.Number)
			if err := touch(filepath.Join(outDir, rd.SegmentRoot, rel)); err != nil {
				return err
			}
		}
	}
	return nil
}

// expandSimple resolves only the $Number$ and $Number%0nd$ placeholders used
// by the generated fixtures, avoiding an import cycle with the segments
// package.
func expandSimple(tpl string, number int) string {
	var out []byte
	for i := 0; i < len(tpl); {
		open := indexByte(tpl[i:], '$')
		if open < 0 {
			out = append(out, tpl[i:]...)
			break
		}
		out = append(out, tpl[i:i+open]...)
		open += i
		closeRel := indexByte(tpl[open+1:], '$')
		close := open + 1 + closeRel
		token := tpl[open : close+1]
		switch token {
		case "$Number$":
			out = append(out, fmt.Sprintf("%d", number)...)
		default: // $Number%05d$ style, fixtures always use width 5
			out = append(out, fmt.Sprintf("%05d", number)...)
		}
		i = close + 1
	}
	return string(out)
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func touch(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create segment placeholder %s: %w", path, err)
	}
	return f.Close()
}
