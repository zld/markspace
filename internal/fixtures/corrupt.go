package fixtures

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
)

// --- fault-injection samples ---

// CorruptClasses lists the generated fault classes in stable order.
var CorruptClasses = []string{
	"truncated_stbl", "oob_offset", "bad_crop", "conflict_color", "overflow_ts",
}

func buildCorrupt(dir string, seed int64, specs []trackSpec) (map[string]string, error) {
	out := map[string]string{}
	write := func(class, name string, data []byte) error {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return err
		}
		out[class] = p
		return nil
	}
	base := buildMP4(seed, 0, specs[0])

	// 1. Truncated sample table: ancestor boxes are resized
	// consistently and the file is cut inside stsz, so the parse
	// fault pinpoints the damaged sample table itself.
	stszPos := bytes.Index(base, []byte("stsz")) - 4
	if stszPos < 0 {
		stszPos = len(base) / 2
	}
	newLen := stszPos + 20 // cuts inside the stsz box
	trunc := append([]byte{}, base[:newLen]...)
	for _, typ := range []string{"moov", "trak", "mdia", "minf", "stbl"} {
		if pos := bytes.Index(trunc, []byte(typ)); pos >= 4 {
			copy(trunc[pos-4:], u32(uint32(newLen-(pos-4))))
		}
	}
	if err := write("truncated_stbl", "truncated_stbl.mp4", trunc); err != nil {
		return nil, err
	}

	// 2. Out-of-range chunk offset: point stco entry far beyond EOF.
	oob := append([]byte{}, base...)
	if i := bytes.Index(oob, []byte("stco")); i >= 0 {
		copy(oob[i+12:], u32(uint32(len(base))+4096))
	}
	if err := write("oob_offset", "oob_offset.mp4", oob); err != nil {
		return nil, err
	}

	// 3. Malformed crop: width exceeds the coded frame width.
	badCropSpec := specs[1]
	badCropSpec.crop = &[4]uint32{8, 8, badCropSpec.width + 100, 200}
	if err := write("bad_crop", "bad_crop.mp4", buildMP4(seed, 1, badCropSpec)); err != nil {
		return nil, err
	}

	// 4. Conflicting colour labels: two different colour KLVs; the
	// compose policy must resolve and record the choice.
	conflictSpec := specs[4]
	conflictSpec.colors = [][3]uint16{{5, 6, 6}, {1, 1, 1}}
	if err := write("conflict_color", "conflict_color.mxf", buildMXF(seed, 4, conflictSpec)); err != nil {
		return nil, err
	}

	// 5. Track timestamp overflow: a media offset near int64 max makes
	// the unified-tick conversion overflow during alignment.
	ovfSpec := specs[6]
	ovfSpec.mediaOffset = math.MaxInt64 - 100
	if err := write("overflow_ts", "overflow_ts.mxf", buildMXF(seed, 6, ovfSpec)); err != nil {
		return nil, err
	}
	return out, nil
}
