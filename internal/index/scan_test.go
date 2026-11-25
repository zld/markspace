package index_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"mediacore/internal/diagnostic"
	"mediacore/internal/fixture"
	"mediacore/internal/index"
	"mediacore/internal/manifest"
)

func normalBundle(t *testing.T) (string, *fixture.Bundle) {
	t.Helper()
	root := t.TempDir()
	b, err := fixture.GenerateAll(filepath.Join(root, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	return root, b
}

func TestScanAndQueryExactRangeZeroDeviations(t *testing.T) {
	root, b := normalBundle(t)
	d, err := manifest.ParseDASH(b.NormalDASH, root)
	if err != nil {
		t.Fatal(err)
	}
	e, err := d.EntryAt("video", 0)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.ScanEntry(e, index.DefaultScanOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Samples) != 2 || idx.Digest == "" {
		t.Fatalf("bad index: samples=%d digest=%q", len(idx.Samples), idx.Digest)
	}
	idx2, err := index.ScanEntry(e, index.DefaultScanOptions())
	if err != nil {
		t.Fatal(err)
	}
	if idx.Digest != idx2.Digest {
		t.Fatal("index digest is not stable across scans")
	}

	r, err := index.QueryIndex(idx, e.ProgramStartU, e.ProgramEndU)
	if err != nil {
		t.Fatal(err)
	}
	if r.SampleCount != 2 {
		t.Fatalf("sample count=%d", r.SampleCount)
	}
	if r.FirstTickDeviation != 0 || r.LastTickDeviation != 0 ||
		r.FirstByteDeviation != 0 || r.LastByteDeviation != 0 {
		t.Fatalf("unexpected deviations: tick=[%d,%d] byte=[%d,%d]",
			r.FirstTickDeviation, r.LastTickDeviation, r.FirstByteDeviation, r.LastByteDeviation)
	}

	// Exact second-sample boundary: [20ms,40ms).
	r2, err := index.QueryIndex(idx, 20_000_000, 40_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if r2.SampleCount != 1 || r2.FirstTick != 20_000_000 {
		t.Fatalf("exact boundary query returned n=%d first=%d", r2.SampleCount, r2.FirstTick)
	}
	if r2.FirstByteDeviation != 0 {
		t.Fatalf("contiguous byte gap=%d", r2.FirstByteDeviation)
	}

	var bytesRead int64
	_, err = index.ReadRangeBytes(context.Background(), r, 7, func(p []byte) error {
		bytesRead += int64(len(p))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytesRead != int64(r.Samples[0].ByteSize+r.Samples[1].ByteSize) {
		t.Fatalf("read %d bytes, want sample sizes", bytesRead)
	}
}

func TestQueryRefusesMisalignedRepresentation(t *testing.T) {
	root, b := normalBundle(t)
	d, err := manifest.ParseDASH(b.NormalDASH, root)
	if err != nil {
		t.Fatal(err)
	}
	e, err := d.EntryAt("wide", 0)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := index.ScanEntry(e, index.DefaultScanOptions())
	if err != nil {
		t.Fatal(err)
	}
	_, err = index.QueryIndex(idx, e.ProgramStartU, e.ProgramEndU)
	if !errors.Is(err, diagnostic.ErrMisaligned) {
		t.Fatalf("misaligned query err=%v", err)
	}
}

func TestScanTruncatedAndIllegalSampleLocations(t *testing.T) {
	root, b := normalBundle(t)

	for _, tc := range []struct {
		name string
		c    fixture.ManifestCase
		kind error
	}{
		{"truncated", b.Truncated, diagnostic.ErrTruncated},
		{"illegal", b.IllegalSample, diagnostic.ErrIllegalSample},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, err := manifest.ParseDASH(tc.c.Manifest, root)
			if err != nil {
				t.Fatal(err)
			}
			e, err := d.EntryAt(tc.c.Representation, tc.c.EntryIndex)
			if err != nil {
				t.Fatal(err)
			}
			_, err = index.ScanEntry(e, index.DefaultScanOptions())
			if !errors.Is(err, tc.kind) {
				t.Fatalf("scan err=%v, want %v", err, tc.kind)
			}
			pos := diagnostic.PositionOf(err)
			if pos.Representation != tc.c.Representation || pos.EntryIndex != tc.c.EntryIndex {
				t.Fatalf("bad location %+v", pos)
			}
			if pos.ByteOffset != tc.c.ByteOffset {
				t.Fatalf("byte offset=%d want %d", pos.ByteOffset, tc.c.ByteOffset)
			}
		})
	}
}
