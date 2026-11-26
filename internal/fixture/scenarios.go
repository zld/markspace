package fixture

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// Scenario names accepted by the demo command.
const (
	ScenarioNormal   = "normal"
	ScenarioDamage   = "damage"
	ScenarioReplace  = "replace"
	ScenarioMetadata = "metadata"
)

// AllScenarios is the canonical order used when the demo runs the full set.
var AllScenarios = []string{
	ScenarioNormal, ScenarioDamage, ScenarioReplace, ScenarioMetadata,
}

// ScenarioPaths maps a scenario name to its on-disk MP4 working copy.
func ScenarioPaths(base *Bundle, workDir, scenario string) string {
	return filepath.Join(workDir, scenario+".fmp4")
}

// Materialize copies the base fragmented MP4 into per-scenario working files and
// applies the mutation each scenario is meant to exercise. The returned map
// gives the MP4 path for every requested scenario.
func Materialize(base *Bundle, workDir string, scenarios []string) (map[string]string, error) {
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, sc := range scenarios {
		dst := ScenarioPaths(base, workDir, sc)
		data, err := os.ReadFile(base.MP4Path)
		if err != nil {
			return nil, err
		}
		switch sc {
		case ScenarioNormal:
			// untouched copy
		case ScenarioDamage:
			data = corruptMdatPayload(data)
		case ScenarioReplace:
			// A replacement changes content bytes (new visual pattern), which
			// must produce a new source hash and force a re-index.
			data = append(data, 0x00)
		case ScenarioMetadata:
			// A container/metadata-only edit: append a free box. Media sample
			// bytes are untouched so derived segment content stays reusable.
			data = appendFreeBox(data)
		default:
			return nil, fmt.Errorf("fixture: unknown scenario %q", sc)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return nil, err
		}
		paths[sc] = dst
	}
	return paths, nil
}

// corruptMdatPayload flips bytes inside the first mdat payload, leaving the
// box structure valid so the demuxer still walks it but the affected samples
// fail checksum verification. The offset of the corrupted range is returned
// implicitly via the box header that precedes it.
func corruptMdatPayload(data []byte) []byte {
	out := append([]byte(nil), data...)
	off, hdr, ok := findBox(out, "mdat")
	if !ok {
		// Fall back to corrupting the last quarter if no mdat is found.
		start := len(out) * 3 / 4
		for i := start; i < len(out); i++ {
			out[i] ^= 0xFF
		}
		return out
	}
	payloadStart := off + hdr
	// Corrupt a bounded window well inside the payload.
	end := min(boxSize(out, off, hdr), int64(len(out)))
	mid := int64(payloadStart) + (end-int64(payloadStart))/2
	for i := mid; i < mid+16 && i < end; i++ {
		out[i] ^= 0xA5
	}
	return out
}

// appendFreeBox appends an ISO 'free' box, changing container bytes without
// touching any media sample content.
func appendFreeBox(data []byte) []byte {
	free := make([]byte, 16)
	binary.BigEndian.PutUint32(free, 16)
	copy(free[4:8], "free")
	return append(data, free...)
}

// findBox returns the absolute offset and header size of the first box with
// the given four-character type.
func findBox(data []byte, typ string) (off int, headerSize int, ok bool) {
	i := 0
	for i+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[i:]))
		t := string(data[i+4 : i+8])
		if t == typ {
			return i, 8, true
		}
		if size <= 8 {
			return 0, 0, false
		}
		i += size
	}
	return 0, 0, false
}

func boxSize(data []byte, off, hdr int) int64 {
	return int64(binary.BigEndian.Uint32(data[off:]))
}
