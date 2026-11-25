package fixture

import (
	"fmt"
	"strconv"
	"strings"
)

// parseFragmentName extracts the numeric suffix from names produced by
// FragmentName (and the duplicate arrivals' frag-1xx.m4s names).
func parseFragmentName(name string) (uint32, error) {
	const prefix = "frag-"
	const suffix = ".m4s"
	if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, suffix) {
		return 0, fmt.Errorf("fixture: unexpected fragment name %q", name)
	}
	mid := name[len(prefix) : len(name)-len(suffix)]
	v, err := strconv.ParseUint(mid, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("fixture: invalid fragment name %q: %w", name, err)
	}
	return uint32(v), nil
}

// IsFragmentName reports whether name looks like a generated fragment file.
func IsFragmentName(name string) bool {
	_, err := parseFragmentName(name)
	return err == nil
}
