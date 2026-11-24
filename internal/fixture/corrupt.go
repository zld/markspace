package fixture

import (
	"encoding/binary"
	"fmt"
	"os"
)

// truncateSampleTable inflates the stco entry count without adding bytes, so
// the sample table claims more chunk offsets than the box carries. This models
// a power loss during an index flush: the count survives, entries do not.
func truncateSampleTable(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	pos := indexOf(data, "stco")
	if pos < 0 {
		return fmt.Errorf("stco not found")
	}
	countOff := pos + 8 // type(4) + version/flags(4)
	if countOff+4 > len(data) {
		return fmt.Errorf("stco header truncated")
	}
	n := binary.BigEndian.Uint32(data[countOff:])
	binary.BigEndian.PutUint32(data[countOff:], n+64)
	return os.WriteFile(path, data, 0o644)
}

// overrunEdit inflates the elst segment duration so the editorial interval is
// longer than the media actually contains.
func overrunEdit(path string, t *Table) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	pos := indexOf(data, "elst")
	if pos < 0 {
		return fmt.Errorf("elst not found")
	}
	// Layout after type: version/flags(4) entry_count(4) seg_duration(4)
	// media_time(4) rate(4). Rewrite seg_duration to a value far beyond the
	// media duration.
	durOff := pos + 4 + 4 + 4
	if durOff+4 > len(data) {
		return fmt.Errorf("elst truncated")
	}
	v := binary.BigEndian.Uint32(data[durOff:])
	binary.BigEndian.PutUint32(data[durOff:], v+uint32(60_000))
	return os.WriteFile(path, data, 0o644)
}

func indexOf(data []byte, needle string) int {
	for i := 0; i+len(needle) <= len(data); i++ {
		if string(data[i:i+len(needle)]) == needle {
			return i
		}
	}
	return -1
}
