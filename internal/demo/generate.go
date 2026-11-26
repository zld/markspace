package demo

import (
	"os"
	"path/filepath"

	mp4media "mediapipeline/internal/mp4"
)

// InputFiles contains the generated sample paths for one demo work directory.
type InputFiles struct {
	Dir           string
	Normal        string
	TruncatedMoov string
	BadSTCO       string
	BackwardPTS   string
	WrapBase      string
	WrapChanged   string
}

const (
	normalName      = "normal.mp4"
	truncatedName   = "truncated-moov.mp4"
	badSTCOName     = "bad-stco.mp4"
	backwardPTSName = "backward-pts.mp4"
	wrapBaseName    = "wrap-base.mp4"
	wrapChangedName = "wrap-container-only.mp4"
)

// GenerateInputs writes deterministic Go-built MP4/MOV samples. Existing files
// are overwritten with identical bytes, so restarts are stable.
func GenerateInputs(dir string) (*InputFiles, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	f := &InputFiles{
		Dir:           dir,
		Normal:        filepath.Join(dir, normalName),
		TruncatedMoov: filepath.Join(dir, truncatedName),
		BadSTCO:       filepath.Join(dir, badSTCOName),
		BackwardPTS:   filepath.Join(dir, backwardPTSName),
		WrapBase:      filepath.Join(dir, wrapBaseName),
		WrapChanged:   filepath.Join(dir, wrapChangedName),
	}

	normal, err := mp4media.BuildFixture(mp4media.FixtureSpec{
		SampleCount: 20,
		SampleSize:  200 * 1024,
		SyncNumbers: []uint32{1, 6, 11, 16},
	})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(f.Normal, normal.Data, 0o644); err != nil {
		return nil, err
	}

	full, err := mp4media.BuildFixture(mp4media.FixtureSpec{SampleCount: 6, SampleSize: 128})
	if err != nil {
		return nil, err
	}
	cut := full.STCOEntriesStart + 4
	if cut > len(full.Data) {
		cut = len(full.Data) / 2
	}
	if err := os.WriteFile(f.TruncatedMoov, full.Data[:cut], 0o644); err != nil {
		return nil, err
	}

	bad, err := mp4media.BuildFixture(mp4media.FixtureSpec{
		SampleCount: 6, SampleSize: 128, BadChunkOffset: true, SyncNumbers: []uint32{1},
	})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(f.BadSTCO, bad.Data, 0o644); err != nil {
		return nil, err
	}

	offsets := make([]int32, 8)
	offsets[0] = 6000
	offsets[1] = 0
	backward, err := mp4media.BuildFixture(mp4media.FixtureSpec{
		SampleCount: 8, SampleSize: 128, CompositionOffsets: offsets, SyncNumbers: []uint32{1},
	})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(f.BackwardPTS, backward.Data, 0o644); err != nil {
		return nil, err
	}

	base, err := mp4media.BuildFixture(mp4media.FixtureSpec{
		SampleCount: 6, SampleSize: 256, MinorVersion: 256, SyncNumbers: []uint32{1, 4},
	})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(f.WrapBase, base.Data, 0o644); err != nil {
		return nil, err
	}
	changed := append([]byte(nil), base.Data...)
	// ftyp.minor_version is at offset 12 for this small-box fixture.
	changed[12] = 0x00
	changed[13] = 0x00
	changed[14] = 0x02
	changed[15] = 0x00 // 512
	if err := os.WriteFile(f.WrapChanged, changed, 0o644); err != nil {
		return nil, err
	}
	return f, nil
}
