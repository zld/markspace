package ts_test

import (
	"encoding/binary"
	"testing"

	"medpipe/internal/fault"
	"medpipe/internal/samplegen"
	"medpipe/internal/ts"
)

func submit(t *testing.T, p *ts.PSIParser, pid uint16, section []byte) bool {
	t.Helper()
	committed, err := p.SubmitSection(pid, section)
	if err != nil {
		t.Fatalf("SubmitSection: %v", err)
	}
	return committed
}

func TestMPEGCRC32CheckValue(t *testing.T) {
	if got := ts.CRC32MPEG([]byte("123456789")); got != 0x0376e6e7 {
		t.Fatalf("CRC=%08x want 0376e6e7", got)
	}
}

func TestPATPMTVersionCommit(t *testing.T) {
	p := ts.NewPSIParser()
	if !submit(t, p, 0, samplegen.BuildPAT(0, samplegen.PMTPID)) {
		t.Fatal("PAT v0 did not commit")
	}
	if !submit(t, p, samplegen.PMTPID, samplegen.BuildPMT(0, samplegen.PMTPID, samplegen.VideoPID, samplegen.AudioPID)) {
		t.Fatal("PMT v0 did not commit")
	}
	if p.ActivePAT().Version != 0 || len(p.Tracks()) != 2 {
		t.Fatalf("active PAT=%+v tracks=%+v", p.ActivePAT(), p.Tracks())
	}
}

func TestBadCRCAndIncompleteVersionRetainOldMapping(t *testing.T) {
	p := ts.NewPSIParser()
	submit(t, p, 0, samplegen.BuildPAT(0, samplegen.PMTPID))
	submit(t, p, samplegen.PMTPID, samplegen.BuildPMT(0, samplegen.PMTPID, samplegen.VideoPID, samplegen.AudioPID))

	bad := samplegen.BuildPAT(1, samplegen.PMTPID)
	bad[len(bad)-1] ^= 0xff
	if _, err := p.SubmitSection(0, bad); err == nil || err.Code != fault.ErrBadCRC {
		t.Fatalf("bad CRC err=%v", err)
	}
	if p.ActivePAT().Version != 0 {
		t.Fatalf("bad CRC overwrote active version=%d", p.ActivePAT().Version)
	}

	// Announce v1 as a two-section table but supply only section 0.
	missing := samplegen.BuildPAT(1, samplegen.PMTPID)
	missing[7] = 1 // last_section_number
	crc := ts.CRC32MPEG(missing[:len(missing)-4])
	binary.BigEndian.PutUint32(missing[len(missing)-4:], crc)
	if committed := submit(t, p, 0, missing); committed {
		t.Fatal("incomplete section set committed")
	}
	if p.ActivePAT().Version != 0 {
		t.Fatal("incomplete version overwrote old mapping")
	}
	if events := p.MissingVersions(); len(events) == 0 {
		t.Fatal("missing version event not reported")
	}

	good := samplegen.BuildPAT(2, samplegen.PMTPID)
	submit(t, p, 0, good)
	if p.ActivePAT().Version != 2 {
		t.Fatalf("version=%d want 2 after complete replacement table", p.ActivePAT().Version)
	}
}

func TestUnknownCodecIsReportedByMapping(t *testing.T) {
	_, _, ok := ts.CodecForStreamType(0xfe)
	if ok {
		t.Fatal("unknown stream type unexpectedly supported")
	}
	if codec, kind, ok := ts.CodecForStreamType(ts.StreamTypeH264); !ok || codec != "avc1" || kind != "video" {
		t.Fatalf("h264 mapping=%s/%s ok=%v", codec, kind, ok)
	}
}
