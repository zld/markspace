package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"streamcheck/internal/manifest"
)

// VerifyManifest re-reads a written playlist and checks it against the
// expected entries and the files on disk. It returns the SHA-256 of the file
// and every inconsistency found.
func VerifyManifest(path string, expected []manifest.PlaylistSegment, inputDir string) (string, []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", []string{fmt.Sprintf("read_manifest %s: %v", filepath.Base(path), err)}
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	var issues []string
	p, err := manifest.Parse(data)
	if err != nil {
		return digest, []string{fmt.Sprintf("parse_manifest %s: %v", filepath.Base(path), err)}
	}
	if len(p.Segments) != len(expected) {
		issues = append(issues, fmt.Sprintf("manifest_entry_count %s: %d, want %d",
			filepath.Base(path), len(p.Segments), len(expected)))
	}
	firstID := 0
	if len(expected) > 0 {
		firstID = expected[0].ID
	}
	if p.MediaSequence != firstID {
		issues = append(issues, fmt.Sprintf("media_sequence %d, want %d", p.MediaSequence, firstID))
	}
	for i := 0; i < len(p.Segments) && i < len(expected); i++ {
		got, want := p.Segments[i], expected[i]
		if got.FileName != want.FileName {
			issues = append(issues, fmt.Sprintf("manifest_order entry %d: %q, want %q", i, got.FileName, want.FileName))
		}
		if got.DurationMS != want.DurationMS {
			issues = append(issues, fmt.Sprintf("manifest_duration %q: %dms, want %dms",
				got.FileName, got.DurationMS, want.DurationMS))
		}
		full, err := SafeJoin(inputDir, got.FileName)
		if err != nil {
			issues = append(issues, fmt.Sprintf("unsafe_path %q: %v", got.FileName, err))
			continue
		}
		fi, err := os.Stat(full)
		if err != nil {
			issues = append(issues, fmt.Sprintf("manifest_missing_file %q", got.FileName))
			continue
		}
		if !fi.Mode().IsRegular() {
			issues = append(issues, fmt.Sprintf("manifest_not_regular %q", got.FileName))
		}
	}
	return digest, issues
}
