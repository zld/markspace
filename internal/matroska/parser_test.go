package matroska_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"mediacheck/internal/ebml"
	"mediacheck/internal/fixture"
	"mediacheck/internal/matroska"
)

func parse(t *testing.T, doc []byte) (*matroska.Parser, *ebml.Element, *ebml.Reader) {
	t.Helper()
	lim := ebml.DefaultLimits()
	r := ebml.NewReader(bytes.NewReader(doc), lim)
	p := matroska.NewParser(r, lim)
	seg, err := p.ParseHeader(int64(len(doc)))
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	return p, seg, r
}

func TestParseCleanStreamAndBlockAttribution(t *testing.T) {
	opt := fixture.Options{Seed: 1, Tracks: 2, Clusters: 4, BlocksPerClus: 3}
	doc := fixture.Generate(opt)
	p, seg, _ := parse(t, doc)

	if len(p.Tracks()) != 2 {
		t.Fatalf("tracks=%d want 2", len(p.Tracks()))
	}
	var clusters []matroska.ClusterScan
	err := p.ScanClusters(seg, int64(len(doc)), func(cs matroska.ClusterScan) error {
		clusters = append(clusters, cs)
		return nil
	})
	if err != nil {
		t.Fatalf("ScanClusters: %v", err)
	}
	if len(clusters) != 4 {
		t.Fatalf("clusters=%d want 4", len(clusters))
	}
	// Interleaved blocks must be attributed back to both track numbers.
	seen := map[uint64]bool{}
	keyframes := 0
	for _, cs := range clusters {
		if len(cs.Blocks) != 6 { // 3 video + 3 audio
			t.Fatalf("block count=%d want 6", len(cs.Blocks))
		}
		for _, b := range cs.Blocks {
			seen[b.TrackNumber] = true
			if b.Keyframe {
				keyframes++
			}
			if b.PayloadSize == 0 {
				t.Fatal("zero payload size")
			}
		}
	}
	if !seen[1] || !seen[2] || keyframes != 4 {
		t.Fatalf("attribution seen=%v keyframes=%d want 4", seen, keyframes)
	}
}

func TestUnknownLengthSegmentAndClusters(t *testing.T) {
	opt := fixture.Options{Seed: 2, Tracks: 1, Clusters: 3, BlocksPerClus: 2, UnknownLen: true}
	doc := fixture.Generate(opt)
	p, seg, _ := parse(t, doc)
	if !seg.UnknownSize {
		t.Fatal("segment should be unknown-length")
	}
	n := 0
	err := p.ScanClusters(seg, int64(len(doc)), func(cs matroska.ClusterScan) error {
		n += len(cs.Blocks)
		return nil
	})
	if err != nil {
		t.Fatalf("unknown-length scan: %v", err)
	}
	if n != 6 {
		t.Fatalf("blocks=%d want 6", n)
	}
}

func TestClusterTruncationReportsFirstIllegalByte(t *testing.T) {
	opt := fixture.Options{Seed: 3, Tracks: 1, Clusters: 4, BlocksPerClus: 3,
		Corrupt: fixture.CorruptClusterTruncate}
	doc := fixture.Generate(opt)
	lim := ebml.DefaultLimits()
	r := ebml.NewReader(bytes.NewReader(doc), lim)
	p := matroska.NewParser(r, lim)
	seg, err := p.ParseHeader(int64(len(doc)))
	if err != nil {
		t.Fatalf("header parse should survive: %v", err)
	}
	scanErr := p.ScanClusters(seg, int64(len(doc)), func(matroska.ClusterScan) error { return nil })
	var pe *matroska.ParseError
	if !errors.As(scanErr, &pe) {
		t.Fatalf("want ParseError, got %v", scanErr)
	}
	if pe.Class != ebml.ErrTruncated {
		t.Fatalf("class=%s want TRUNCATED", pe.Class)
	}
	// The first illegal byte is exactly the file length: the reader asked for
	// a byte the document does not contain.
	if pe.Offset != int64(len(doc)) && pe.Offset != int64(len(doc))-1 {
		t.Fatalf("offset=%d fileLen=%d (must be within 1 byte)", pe.Offset, len(doc))
	}
}

func TestMissingTrackReferenceFailsClosed(t *testing.T) {
	opt := fixture.Options{Seed: 4, Tracks: 1, Clusters: 2, BlocksPerClus: 2,
		Corrupt: fixture.CorruptMissingTrack}
	doc := fixture.Generate(opt)
	p, seg, _ := parse(t, doc)
	err := p.ScanClusters(seg, int64(len(doc)), func(matroska.ClusterScan) error {
		t.Fatal("no cluster may be emitted for an unknown-track block stream")
		return nil
	})
	var pe *matroska.ParseError
	if !errors.As(err, &pe) || pe.TrackNumber != 9 {
		t.Fatalf("want ParseError track=9, got %v", err)
	}
}

func TestEmptyInputAndGarbage(t *testing.T) {
	lim := ebml.DefaultLimits()
	r := ebml.NewReader(bytes.NewReader(nil), lim)
	p := matroska.NewParser(r, lim)
	if _, err := p.ParseHeader(0); !errors.Is(err, io.EOF) && err == nil {
		t.Fatalf("empty input must error, got %v", err)
	}

	r2 := ebml.NewReader(bytes.NewReader([]byte{0x00, 0x00, 0x00}), lim)
	p2 := matroska.NewParser(r2, lim)
	_, err := p2.ParseHeader(3)
	var pe *matroska.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("garbage must produce ParseError, got %v", err)
	}
}
