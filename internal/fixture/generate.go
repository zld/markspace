package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
)

// Manifest records exactly what generate wrote, including byte positions that
// tests compare parser error offsets against.
type Manifest struct {
	Valid            string `json:"valid"`
	Damaged          string `json:"damaged"`
	DamagedOffset    int64  `json:"damaged_offset"`
	BadCrop          string `json:"bad_crop"`
	BadCropOffset    int64  `json:"bad_crop_offset"`
	Boundary         string `json:"boundary"`
	MXF              string `json:"mxf"`
	MXFConflict      string `json:"mxf_conflict"`
	MXFDamaged       string `json:"mxf_damaged"`
	MXFDamagedOffset int64  `json:"mxf_damaged_offset"`
}

// Generate writes every fixture variant into dir and returns the manifest.
// Files are deterministic: the same call always produces identical bytes.
func Generate(dir string) (*Manifest, error) {
	m := &Manifest{
		Valid:       filepath.Join(dir, "valid.mp4"),
		Damaged:     filepath.Join(dir, "damaged.mp4"),
		BadCrop:     filepath.Join(dir, "badcrop.mp4"),
		Boundary:    filepath.Join(dir, "boundary.mp4"),
		MXF:         filepath.Join(dir, "valid.mxf"),
		MXFConflict: filepath.Join(dir, "conflict.mxf"),
		MXFDamaged:  filepath.Join(dir, "damaged.mxf"),
	}

	valid, _ := defaultSpec().buildMP4()
	if err := WriteFile(m.Valid, valid); err != nil {
		return nil, err
	}

	damaged, damageAt := truncate(valid, len(valid)-37)
	if err := WriteFile(m.Damaged, damaged); err != nil {
		return nil, err
	}
	m.DamagedOffset = int64(damageAt)

	badSpec := defaultSpec()
	badSpec.crop = &cropBox{wN: 2100, wD: 1, hN: 1080, hD: 1, offXD: 1, offYD: 1}
	bad, _ := badSpec.buildMP4()
	if err := WriteFile(m.BadCrop, bad); err != nil {
		return nil, err
	}
	m.BadCropOffset = findBoxOffset(bad, "clap")

	boundarySpec := defaultSpec()
	boundarySpec.videoSamples = 90
	boundarySpec.videoDelta = 1500 // 60 fps at 90k
	boundarySpec.audioPackets = 128
	boundarySpec.audioAmplitude = func(i int) int16 {
		ms := int64(i) * 1024 * 1000 / 48000
		if ms < 800 {
			return 0
		}
		return 3000
	}
	boundary, _ := boundarySpec.buildMP4()
	if err := WriteFile(m.Boundary, boundary); err != nil {
		return nil, err
	}

	mxf, err := buildMXF(false, false)
	if err != nil {
		return nil, err
	}
	if err := WriteFile(m.MXF, mxf); err != nil {
		return nil, err
	}
	mxfConflict, err := buildMXF(true, false)
	if err != nil {
		return nil, err
	}
	if err := WriteFile(m.MXFConflict, mxfConflict); err != nil {
		return nil, err
	}
	mxfBad, off, err := buildDamagedMXF()
	if err != nil {
		return nil, err
	}
	if err := WriteFile(m.MXFDamaged, mxfBad); err != nil {
		return nil, err
	}
	m.MXFDamagedOffset = off

	meta, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := WriteFile(filepath.Join(dir, "manifest.json"), append(meta, '\n')); err != nil {
		return nil, err
	}
	return m, nil
}

// truncate cuts data at n bytes and returns the new file plus the position of
// the first missing byte, which is what a parser must report.
func truncate(data []byte, n int) ([]byte, int) {
	out := append([]byte(nil), data[:n]...)
	return out, n
}

// findBoxOffset locates a box by type and returns its absolute offset.
func findBoxOffset(data []byte, typ string) int64 {
	for i := 0; i+8 <= len(data); i++ {
		if string(data[i+4:i+8]) == typ {
			return int64(i)
		}
	}
	return -1
}

// DigestFile is a small helper shared by tests.
func DigestFile(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
