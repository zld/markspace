package manifest

import (
	"crypto/sha256"
	"encoding/hex"

	"example.com/assetcheck/internal/timeline"
)

func digestOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalRate(track uint64) string   { return "audio." + uitoa(track) + ".sample_rate_hz" }
func canonicalLayout(track uint64) string { return "audio." + uitoa(track) + ".channel_layout" }
func canonicalColour(track uint64) string { return "video." + uitoa(track) + ".colour" }
func canonicalTrack(track uint64) string  { return "tracks." + uitoa(track) }

func uitoa(v uint64) string { return fmtInt(int64(v)) }

// FieldPaths lists every canonical field in a stable order, for audit output.
func (m *Manifest) FieldPaths() []string {
	paths := make([]string, 0, len(m.FieldMap))
	for k := range m.FieldMap {
		paths = append(paths, k)
	}
	sortStrings(paths)
	return paths
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// IsVersionError distinguishes version rejection from field mapping failures.
func IsVersionError(err error) bool {
	return timeline.CategoryOf(err) == timeline.CatManifestVersion
}

// IsFieldError reports whether the failure is an unmappable/unknown field.
func IsFieldError(err error) bool {
	return timeline.CategoryOf(err) == timeline.CatManifestField
}
