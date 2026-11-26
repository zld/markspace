package fixture

import (
	"testing"

	"continuity/internal/manifest"
	"continuity/internal/media"
	rtprec "continuity/internal/rtp"
)

func TestBuildCleanFixture(t *testing.T) {
	s, err := Build("clean", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := manifest.ParseHLSBundle(s.Entry, s.Root)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Segments) != 8 {
		t.Fatalf("segments=%d", len(b.Segments))
	}
	pkts, err := rtprec.ReadRecords(s.RTPPath)
	if err != nil {
		t.Fatal(err)
	}
	rep := rtprec.RecoverPackets(pkts)
	if rep.Recovered != 4 || rep.Unrecoverable != 0 || rep.RecoveryRate != 100 {
		t.Fatalf("rtp=%+v", rep)
	}
}

func TestBuildFaultFixtures(t *testing.T) {
	d, err := Build("damaged", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if d.TruncationOffset <= 0 || d.OOBReference == "" {
		t.Fatalf("damaged spec=%+v", d)
	}
	if _, err := media.ReadFragment(d.TruncatedFile, 90000); err == nil {
		t.Fatal("truncated fixture did not fail")
	}

	b, err := Build("boundary", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if b.OverflowOffset == 0 || b.Unrecoverable != 2 {
		t.Fatalf("boundary spec=%+v", b)
	}
	pkts, err := rtprec.ReadRecords(b.RTPPath)
	if err != nil {
		t.Fatal(err)
	}
	rep := rtprec.RecoverPackets(pkts)
	if rep.Unrecoverable != 2 || len(rep.Gaps) != 1 {
		t.Fatalf("report=%+v", rep)
	}
}
