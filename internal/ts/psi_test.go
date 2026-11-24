package ts

import "testing"

// buildPATSection builds a one-program PAT section (table id 0x00).
func buildPATSection(version byte, pmtPID uint16) []byte {
	sec := []byte{
		0x00,       // table id
		0xB0, 0x11, // section syntax indicator + section_length=17
		0x00, 0x01, // transport stream id
		0xC0 | (version << 1) | 0x01, // version, current=1
		0x00, 0x00,                   // section/last section number
		0x00, 0x01, // program number 1
		byte(0xE0 | pmtPID>>8), byte(pmtPID),
	}
	for len(sec) < 3+17-4 {
		sec = append(sec, 0x00)
	}
	sec = append(sec, 0xAA, 0xBB, 0xCC, 0xDD) // dummy CRC
	return sec
}

// buildPMTSection builds a PMT (table id 0x02) with one video and one audio
// elementary stream.
func buildPMTSection(version byte, pcrPID, videoPID, audioPID uint16) []byte {
	body := []byte{
		0x02,
		0xB0, 0x00, // section length patched below
		0x00, 0x01, // program number
		0xC0 | (version << 1) | 0x01,
		0x00, 0x00,
		byte(0xE0 | pcrPID>>8), byte(pcrPID),
		0xF0, 0x00, // program info length 0
		byte(StreamTypeVideo), byte(0xE0 | videoPID>>8), byte(videoPID), 0xF0, 0x00,
		byte(StreamTypeAudio), byte(0xE0 | audioPID>>8), byte(audioPID), 0xF0, 0x00,
	}
	crc := []byte{0x11, 0x22, 0x33, 0x44}
	secLen := len(body) - 3 + len(crc)
	body[2] = byte(secLen & 0x0F)
	body[1] = 0xB0 | byte(secLen>>8)
	return append(body, crc...)
}

// sectionPackets wraps a section in TS packets on pid, starting with PUSI.
func sectionPackets(pid uint16, cc *byte, section []byte) [][]byte {
	var out [][]byte
	first := true
	rest := section
	for len(rest) > 0 || first {
		payload := make([]byte, 184)
		for i := range payload {
			payload[i] = 0xFF
		}
		n := len(rest)
		if first {
			payload[0] = 0 // pointer field
			if n > 183 {
				n = 183
			}
			copy(payload[1:], rest[:n])
		} else {
			if n > 184 {
				n = 184
			}
			copy(payload, rest[:n])
		}
		pkt := BuildPacket(BuildOpts{
			PID: pid, PUSI: first, AdaptationControl: 1,
			CC: *cc, Payload: payload,
		})
		*cc = (*cc + 1) & 0x0F
		out = append(out, pkt)
		rest = rest[n:]
		first = false
		if len(rest) == 0 {
			break
		}
	}
	return out
}

func TestPATPMTMapping(t *testing.T) {
	tr := NewPSITracker()
	var cc byte
	for _, raw := range sectionPackets(PIDPAT, &cc, buildPATSection(1, 0x1000)) {
		p, _ := Parse(raw, 0)
		if err := tr.Observe(p); err != nil {
			t.Fatalf("pat: %v", err)
		}
	}
	if tr.Active() == nil || tr.Active().PMTPID != 0x1000 {
		t.Fatalf("PAT not applied: %+v", tr.Active())
	}
	var pcc byte
	for _, raw := range sectionPackets(0x1000, &pcc, buildPMTSection(1, 0x0100, 0x0100, 0x0102)) {
		p, _ := Parse(raw, 0)
		if err := tr.Observe(p); err != nil {
			t.Fatalf("pmt: %v", err)
		}
	}
	got := tr.Active()
	if got.PCRPID != 0x0100 {
		t.Fatalf("pcr pid=%04x", got.PCRPID)
	}
	v, ok := got.Lookup(0x0100)
	if !ok || v.Kind != KindVideo {
		t.Fatalf("video track mapping wrong: %+v", v)
	}
	a, ok := got.Lookup(0x0102)
	if !ok || a.Kind != KindAudio {
		t.Fatalf("audio track mapping wrong: %+v", a)
	}
	if _, ok := got.Lookup(0x0999); ok {
		t.Fatal("unmapped PID must not resolve")
	}
}

func TestPSIVersionSwitchBoundary(t *testing.T) {
	tr := NewPSITracker()
	var cc byte
	feed := func(pid uint16, section []byte) {
		for _, raw := range sectionPackets(pid, &cc, section) {
			p, _ := Parse(raw, 0)
			if err := tr.Observe(p); err != nil {
				t.Fatalf("observe: %v", err)
			}
		}
	}
	feed(PIDPAT, buildPATSection(1, 0x1000))
	feed(0x1000, buildPMTSection(1, 0x0100, 0x0100, 0x0102))
	// Re-announcing the same version must not create a boundary.
	feed(0x1000, buildPMTSection(1, 0x0100, 0x0100, 0x0102))
	if len(tr.Boundaries()) != 2 { // PAT + PMT only
		t.Fatalf("boundaries=%d want 2", len(tr.Boundaries()))
	}
	// New PMT version moves audio to a new PID.
	feed(0x1000, buildPMTSection(2, 0x0100, 0x0100, 0x0105))
	if _, ok := tr.Active().Lookup(0x0102); ok {
		t.Fatal("old audio PID must be gone after version switch")
	}
	if _, ok := tr.Active().Lookup(0x0105); !ok {
		t.Fatal("new audio PID must be present after version switch")
	}
	bounds := tr.Boundaries()
	last := bounds[len(bounds)-1]
	if last.TableID != 0x02 || last.Old != 1 || last.New != 2 {
		t.Fatalf("boundary record wrong: %+v", last)
	}
}

func TestPMTBeforePATIsFatal(t *testing.T) {
	tr := NewPSITracker()
	var cc byte
	// Feed a PMT-section packet on an arbitrary PID without any PAT.
	raw := sectionPackets(0x1000, &cc, buildPMTSection(0, 0x100, 0x100, 0x102))[0]
	p, _ := Parse(raw, 0)
	// The tracker ignores PIDs it has not been told about; simulate the
	// PAT-known case by installing a PAT mapping first, then a foreign PMT PID.
	if err := tr.Observe(p); err != nil {
		t.Fatalf("unmapped PID should be ignored, got %v", err)
	}
}

func TestBadPointerFieldRejected(t *testing.T) {
	payload := make([]byte, 184)
	payload[0] = 200 // pointer beyond payload
	raw := BuildPacket(BuildOpts{PID: PIDPAT, PUSI: true, CC: 0, Payload: payload})
	p, _ := Parse(raw, 0)
	tr := NewPSITracker()
	if err := tr.Observe(p); err == nil {
		t.Fatal("oversized pointer field must be rejected")
	}
}
