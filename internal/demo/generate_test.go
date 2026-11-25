package demo

import (
	"os"
	"path/filepath"
	"testing"

	"mediascan/internal/container"
	"mediascan/internal/dash"
	"mediascan/internal/errs"
	"mediascan/internal/hls"
	"mediascan/internal/rtpcheck"
)

func TestGenerateDeterministic(t *testing.T) {
	dir1 := t.TempDir()
	b1, err := Generate(dir1)
	if err != nil {
		t.Fatal(err)
	}
	dir2 := t.TempDir()
	b2, err := Generate(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if len(b1.Segments) != len(b2.Segments) || len(b1.Segments) == 0 {
		t.Fatalf("segment counts differ or empty: %d vs %d",
			len(b1.Segments), len(b2.Segments))
	}
	for i := range b1.Segments {
		if b1.Segments[i].ExpectedHash != b2.Segments[i].ExpectedHash {
			t.Fatalf("segment %d hash not deterministic", i)
		}
	}
}

func TestGeneratedDASHParses(t *testing.T) {
	b, err := Generate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m, err := dash.Parse(MPDName, b.ManifestDASH)
	if err != nil {
		t.Fatalf("generated MPD must parse: %v", err)
	}
	if len(m.Representations) != 2 {
		t.Fatalf("representations = %d, want 2", len(m.Representations))
	}
	// Every generated MP4 segment must pass container validation.
	for _, seg := range b.Segments {
		if filepath.Ext(seg.Path) != ".m4s" {
			continue
		}
		data, err := os.ReadFile(seg.Path)
		if err != nil {
			t.Fatal(err)
		}
		c, err := container.Parse(seg.Source, data)
		if err != nil {
			t.Fatalf("generated segment %s must validate: %v", seg.Source, err)
		}
		if len(c.Tracks) != 1 {
			t.Fatalf("segment %s tracks = %d", seg.Source, len(c.Tracks))
		}
	}
}

func TestGeneratedHLSParsesWithDiscontinuity(t *testing.T) {
	b, err := Generate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := hls.Parse(M3U8Name, b.ManifestHLS)
	if err != nil {
		t.Fatalf("generated playlist must parse: %v", err)
	}
	if len(p.DiscontinuityOffsets()) != 1 {
		t.Fatalf("discontinuities = %d, want 1", len(p.DiscontinuityOffsets()))
	}
	// Continuous timeline across the discontinuity: 6000+5500+4250+2000.
	if want := int64(17750); p.TotalDurationMS() != want {
		t.Fatalf("total = %d, want %d", p.TotalDurationMS(), want)
	}
}

func TestGeneratedRTPParsesAndWraps(t *testing.T) {
	b, err := Generate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(b.RTPPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := rtpcheck.ParseStream("stream.rtp", data, 90000)
	if err != nil {
		t.Fatalf("generated RTP must parse: %v", err)
	}
	if len(s.Packets) != 4 {
		t.Fatalf("packets = %d, want 4", len(s.Packets))
	}
	// The second packet must be extended past the 32-bit wrap.
	if s.Packets[1].TimestampExt != 0x100000005 {
		t.Fatalf("wrapped timestamp = %#x", s.Packets[1].TimestampExt)
	}
	if err := s.MarshalRoundTrip("stream.rtp"); err != nil {
		t.Fatalf("rtp round trip: %v", err)
	}
}

func TestCorruptFixturesAreLocated(t *testing.T) {
	b, err := Generate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Corrupt) < 4 {
		t.Fatalf("corrupt fixtures = %d, want >= 4", len(b.Corrupt))
	}
	// The three MP4 corruptions must each fail container validation and
	// report a locatable offset.
	for _, cf := range b.Corrupt {
		data, err := os.ReadFile(cf.Path)
		if err != nil {
			t.Fatal(err)
		}
		switch cf.Kind {
		case "truncated", "illegal-range", "index-missing":
			_, perr := container.Parse(filepath.Base(cf.Path), data)
			se, ok := errs.AsError(perr)
			if !ok {
				t.Fatalf("%s fixture must fail container validation", cf.Kind)
			}
			if se.Offset == errs.Unknown {
				t.Fatalf("%s error must carry an offset", cf.Kind)
			}
		case "manifest":
			// Both manifest corruptions must be rejected with an offset.
			var perr error
			if filepath.Ext(cf.Path) == ".m3u8" {
				_, perr = hls.Parse(filepath.Base(cf.Path), data)
			} else {
				_, perr = dash.Parse(filepath.Base(cf.Path), data)
			}
			se, ok := errs.AsError(perr)
			if !ok || se.Offset == errs.Unknown {
				t.Fatalf("%s manifest must produce located error, got %v",
					cf.Kind, perr)
			}
		}
	}
}

func TestExpectedHashesMatchFiles(t *testing.T) {
	b, err := Generate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range b.Segments {
		data, err := os.ReadFile(seg.Path)
		if err != nil {
			t.Fatal(err)
		}
		if got := expectedRecord(data); got != seg.ExpectedHash {
			t.Fatalf("segment %s hash mismatch", seg.Source)
		}
		if int64(len(data)) != seg.ByteLength {
			t.Fatalf("segment %s length mismatch", seg.Source)
		}
	}
}
