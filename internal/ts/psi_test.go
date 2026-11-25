package ts_test

import (
	"bytes"
	"testing"

	merr "mediapack/internal/errors"
	"mediapack/internal/sample"
	"mediapack/internal/ts"
)

func feedPSI(t *testing.T) (*ts.Tracker, *merr.Book) {
	t.Helper()
	book := merr.NewBook()
	pkts, _ := parseFixture(t)
	tr := ts.NewTracker(book)
	clockTick := uint64(0)
	for _, p := range pkts {
		if p.PCRFlag {
			clockTick = p.PCRBase
		}
		tr.NowTick = clockTick
		tr.Handle(p)
	}
	return tr, book
}

func TestPSIVersionSwitchAndCRC(t *testing.T) {
	tr, book := feedPSI(t)

	crc := book.Of(merr.CatPSICRC)
	if len(crc) != 1 {
		t.Fatalf("psi_crc=%d want 1", len(crc))
	}
	sw := book.Of(merr.CatPSIVersionSwitch)
	if len(sw) != 2 { // PAT + PMT
		t.Fatalf("version switches=%d want 2 (PAT+PMT): %+v", len(sw), sw)
	}
	if tr.Current() == nil || !tr.Current().Complete {
		t.Fatal("final mapping must be complete")
	}
	if tr.Current().VideoPID != sample.PIDVideo || tr.Current().AudioPID != sample.PIDAudio {
		t.Fatalf("mapping pids wrong: %+v", tr.Current())
	}
	if len(tr.History) < 2 {
		t.Fatalf("mapping history=%d want >=2", len(tr.History))
	}
	if len(tr.Gaps) != 1 || !tr.Gaps[0].Resolved {
		t.Fatalf("expected one resolved mapping gap, got %+v", tr.Gaps)
	}
}

func TestPSICRCOffsetMatchesInjection(t *testing.T) {
	L := sample.Generate(sample.DefaultConfig())
	_, book := feedPSI(t)
	crc := book.Of(merr.CatPSICRC)
	if crc[0].Offset != L.Anomalies.BadCRCOffset {
		t.Fatalf("crc issue offset=%d want packet offset %d", crc[0].Offset, L.Anomalies.BadCRCOffset)
	}
	// The bad section must not parse as valid through the public parser.
	rd := ts.NewReader(bytes.NewReader(L.Data[L.Anomalies.BadCRCOffset:L.Anomalies.BadCRCOffset+188]),
		L.Anomalies.BadCRCOffset, nil)
	p, err := rd.Next()
	if err != nil || p == nil {
		t.Fatalf("read bad PMT packet: %v", err)
	}
	secs := ts.NewPSIParser().Sections(p)
	if len(secs) != 1 || ts.ParsePMT(secs[0]).CRCValid {
		t.Fatal("injected bad CRC must be detected by ParsePMT")
	}
}
