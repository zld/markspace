package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediacore/internal/bitstream"
	"mediacore/internal/fault"
	"mediacore/internal/manifest"
	"mediacore/internal/media"
	"mediacore/internal/migrate"
)

// runMigration builds the legacy manifest from the scanned stream, then runs
// the staged migration. The interrupt flag suspends the run after the verify
// stage so the resume path can be demonstrated.
func runMigration(opts Options, stream *bitstream.Stream, syn media.Synthetic) (*migrate.Report, error) {
	dir := filepath.Join(opts.Dir, "manifest")
	legacy, err := legacyManifest(stream, syn)
	if err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(dir, migrate.DefaultOptions(dir).SourceName), legacy); err != nil {
		return nil, err
	}
	mopts := migrate.DefaultOptions(dir)
	if opts.Interrupt && !opts.Resume {
		// Suspend after verification. The rollback point written at that
		// stage lets the next invocation resume without re-appending rows.
		mopts.InterruptAt = migrate.StageVerify
	}
	return migrate.Run(mopts)
}

// legacyManifest renders the scanned frames and generated audio packets as a
// version 1 (millisecond) manifest. Timestamps are exact milliseconds so the
// v1 to v2 normalisation is lossless.
func legacyManifest(stream *bitstream.Stream, syn media.Synthetic) ([]byte, error) {
	type entry struct {
		ID         string `json:"id"`
		Codec      string `json:"codec"`
		Path       string `json:"path"`
		TimeMS     int64  `json:"time_ms"`
		DurationMS int64  `json:"duration_ms"`
	}
	type doc struct {
		Version int     `json:"version"`
		Entries []entry `json:"entries"`
	}
	d := doc{Version: int(manifest.VersionLegacy)}
	frameMS := int64(1000) / int64(syn.FPS)
	for i := range stream.Frames {
		d.Entries = append(d.Entries, entry{
			ID:         "v" + itoa(i),
			Codec:      string(stream.Codec),
			Path:       "frame-" + itoa(i),
			TimeMS:     int64(i) * frameMS,
			DurationMS: frameMS,
		})
	}
	audioMS := int64(1024) * 1000 / 48000
	end := int64(syn.Seconds) * 1000
	for ms := int64(0); ms < end; ms += audioMS {
		d.Entries = append(d.Entries, entry{
			ID:         "a" + itoa(int(ms/audioMS)),
			Codec:      "aac",
			Path:       "audio-" + itoa(int(ms/audioMS)),
			TimeMS:     ms,
			DurationMS: audioMS,
		})
	}
	return json.MarshalIndent(d, "", "  ")
}

func writeFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fault.Newf(fault.CodeInternal, "", "cannot create manifest directory: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fault.Newf(fault.CodeInternal, "", "cannot write manifest: %v", err)
	}
	return nil
}

func itoa(v int) string {
	// Small, allocation-free integer formatting avoids pulling strconv into
	// the hot path of manifest generation.
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
