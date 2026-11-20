package report

import "sort"

// sortEntries orders entries by their slash-normalised path, byte-wise, so
// output is identical across machines and independent of the order in which
// concurrent tasks finished.
func sortEntries(entries []Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return normalizePath(entries[i].Path) < normalizePath(entries[j].Path)
	})
}

func normalizePath(p string) string {
	// Compare on forward slashes only; no case folding, because macOS
	// filesystems are case-insensitive by default but Linux CI is not, and a
	// byte-wise order is the reproducible choice.
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' {
			out = append(out, '/')
			continue
		}
		out = append(out, p[i])
	}
	return string(out)
}
