// Package fixture generates deterministic sample subtitle files used for the
// demo and by end-to-end tests. No external assets are required.
package fixture

import (
	"fmt"
	"os"
	"path/filepath"
)

// SceneSRT is the canonical four-cue sample. Cues 3 and 4 deliberately overlap
// so the overlap policies have something to act on; cue 3 spans exactly frames
// [120,144) at 24000/1001 fps.
const SceneSRT = "1\n" +
	"00:00:00,000 --> 00:00:02,000\n" +
	"opening line\n\n" +
	"2\n" +
	"00:00:02,002 --> 00:00:04,000\n" +
	"second cue\n\n" +
	"3\n" +
	"00:00:05,005 --> 00:00:06,000\n" +
	"third cue\n" +
	"first overlap\n\n" +
	"4\n" +
	"00:00:05,500 --> 00:00:05,900\n" +
	"fourth cue\n" +
	"second overlap\n"

// Files maps file name to content for every sample the fixture command writes.
// scene.ass is deliberately not emitted: the canonical demo converts scene.srt
// to scene.ass, and pre-creating that path would trip the no-overwrite guard.
func Files() map[string]string {
	return map[string]string{
		"scene.srt": SceneSRT,
		"scene.vtt": webVTTSample,
	}
}

// Write creates every sample file inside dir, which must exist.
func Write(dir string) ([]string, error) {
	names := make([]string, 0, 3)
	for name, content := range Files() {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return nil, fmt.Errorf("write fixture %s: %w", path, err)
		}
		names = append(names, path)
	}
	return names, nil
}

const webVTTSample = "WEBVTT\n\n" +
	"cue-1\n" +
	"00:00:00.000 --> 00:00:02.000 align:start position:0%\n" +
	"opening line\n\n" +
	"cue-2\n" +
	"00:00:02.002 --> 00:00:04.000\n" +
	"second cue\n"
