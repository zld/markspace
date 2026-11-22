// Package fixtures also builds the demo corpus: a set of camera-like files
// with known damage classes and a control set that must remain clean.
package fixtures

import (
	"fmt"
	"os"
	"path/filepath"
)

// DemoFile describes one generated demonstration input.
type DemoFile struct {
	Name     string
	Spec     FileSpec
	Damaged  bool
	Expected string // short description of the injected failure
}

// DemoFiles returns the deterministic demo corpus. Every corruption class in
// the specification is represented, and the damaged files keep enough readable
// media to prove partial recovery.
func DemoFiles() []DemoFile {
	avc := func(id uint32, n int, keyEvery int, seed uint64) TrackSpec {
		return TrackSpec{ID: id, Codec: CodecAVC, Timescale: 90000, SampleDur: 3000,
			Samples: n, KeyEvery: keyEvery, SampleSize: 96, SampleSeed: seed}
	}
	audio := func(id uint32, n int, silence []Range, seed uint64) TrackSpec {
		return TrackSpec{ID: id, Codec: CodecPCM, Timescale: 48000, SampleDur: 960,
			Samples: n, KeyEvery: 1, SampleSize: 2, Silence: silence, SampleSeed: seed}
	}
	cleanSpec := func(id uint32, seed uint64, silence []Range) FileSpec {
		return FileSpec{Timescale: 1000, MoovFirst: false, Major: "isom", Brands: []string{"isom", "qt  "},
			BreakStszAt: -1, BreakSizeOf: -1,
			Tracks: []TrackSpec{avc(id, 180, 18, seed), audio(id+100, 180, silence, seed+1)}}
	}
	cleanA := cleanSpec(1, 11, []Range{{Start: 48000, End: 96000}})
	cleanB := cleanSpec(1, 21, nil)

	// Truncated mdat: the tail sample cannot be read.
	truncFull := Build(FileSpec{Timescale: 1000, MoovFirst: true, Major: "isom", Brands: []string{"isom"},
		BreakStszAt: -1, BreakSizeOf: -1,
		Tracks: []TrackSpec{avc(1, 180, 18, 31), audio(101, 180, nil, 32)}})
	trunc := FileSpec{Timescale: 1000, MoovFirst: true, Major: "isom", Brands: []string{"isom"},
		BreakStszAt: -1, BreakSizeOf: -1, TruncateAt: int64(len(truncFull)) - 700,
		Tracks: []TrackSpec{avc(1, 180, 18, 31), audio(101, 180, nil, 32)}}

	// Corrupt sample size: one stsz entry claims more bytes than remain.
	badSize := cleanSpec(1, 41, nil)
	badSize.BreakStszAt = 90

	// 32-bit timestamp wrap.
	wrap := FileSpec{Timescale: 1000, MoovFirst: false, Major: "isom", Brands: []string{"isom"},
		BreakStszAt: -1, BreakSizeOf: -1, WrapAt: 40,
		Tracks: []TrackSpec{{ID: 1, Codec: CodecAVC, Timescale: 90000, SampleDur: 3000,
			Samples: 180, KeyEvery: 18, SampleSize: 96, SampleSeed: 51}}}

	// Missing sync frames after sample 40.
	noSync := cleanSpec(1, 61, nil)
	noSync.DropSyncAfter = 41

	// A camera that writes a 64-bit mdat header and a moov-first layout.
	ext := cleanSpec(1, 71, []Range{{Start: 96000, End: 144000}})
	ext.MoovFirst = true
	ext.ExtendedMdat = true

	return []DemoFile{
		{Name: "cam_a_clean.mp4", Spec: cleanA, Expected: "clean baseline with a silent chapter"},
		{Name: "cam_b_clean.mp4", Spec: cleanB, Expected: "clean baseline"},
		{Name: "cam_c_extended.mp4", Spec: ext, Expected: "moov-first with a 64-bit mdat header"},
		// Duplicate camera feed: a second file with identical track payloads.
		{Name: "cam_b_copy.mp4", Spec: cleanB, Expected: "duplicate feed of cam_b_clean"},
		{Name: "truncated.mp4", Spec: trunc, Damaged: true, Expected: "mdat truncated mid-payload"},
		{Name: "bad_sample_size.mp4", Spec: badSize, Damaged: true, Expected: "stsz entry exceeds file"},
		{Name: "clock_wrap.mp4", Spec: wrap, Damaged: true, Expected: "32-bit timestamp wrap"},
		{Name: "missing_sync.mp4", Spec: noSync, Damaged: true, Expected: "sync table ends early"},
	}
}

// WriteDemo materialises the corpus under dir.
func WriteDemo(dir string) ([]DemoFile, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	files := DemoFiles()
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.Name), Build(f.Spec), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", f.Name, err)
		}
	}
	return files, nil
}
