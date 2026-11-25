package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// ManifestEntry is one accepted representation in the rollback manifest.
type ManifestEntry struct {
	Representation string
	Bytes          int64
	BoundaryCount  int
	Verdict        Verdict
}

// Manifest is the rollback artifact. Only complete, fully-validated layers
// are accepted; its summary is deterministic across repeated runs.
type Manifest struct {
	Entries []ManifestEntry
}

// BuildManifest accepts only the given eligible names; failed or quarantined
// layers never reach this point. Entries are sorted so repeated runs produce
// an identical summary even if callers hand in unsuitable ordering.
func BuildManifest(eligible []ManifestEntry) *Manifest {
	entries := append([]ManifestEntry(nil), eligible...)
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Representation < entries[j].Representation
	})
	return &Manifest{Entries: entries}
}

// Summary renders the canonical manifest text and returns its SHA-256 digest.
// The text includes every entry field, so any content change changes the
// digest while byte-identical reruns stay stable.
func (m *Manifest) Summary() (text, digest string) {
	var b strings.Builder
	for _, e := range m.Entries {
		b.WriteString("entry ")
		b.WriteString(e.Representation)
		b.WriteString(" bytes=")
		b.WriteString(itoa(e.Bytes))
		b.WriteString(" boundaries=")
		b.WriteString(itoaInt(e.BoundaryCount))
		b.WriteString(" verdict=")
		b.WriteString(string(e.Verdict))
		b.WriteString("\n")
	}
	text = b.String()
	sum := sha256.Sum256([]byte(text))
	return text, hex.EncodeToString(sum[:])
}

func itoa(v int64) string { return itoaInt(int(v)) }

func itoaInt(v int) string {
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
