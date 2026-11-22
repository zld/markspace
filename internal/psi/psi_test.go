package psi

import "testing"

// mkSection builds a CRC-valid long-form section.
func mkSection(tableID uint8, ext uint16, version, secNum, lastNum uint8, body []byte) []byte {
	secLen := 5 + len(body) + 4
	s := []byte{
		tableID, 0xB0 | byte(secLen>>8), byte(secLen),
		byte(ext >> 8), byte(ext),
		0xC0 | (version&0x1F)<<1 | 0x01,
		secNum, lastNum,
	}
	s = append(s, body...)
	crc := Checksum(s)
	return append(s, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func patBody(entries ...[2]uint16) []byte {
	var b []byte
	for _, e := range entries {
		b = append(b, byte(e[0]>>8), byte(e[0]), 0xE0|byte(e[1]>>8), byte(e[1]))
	}
	return b
}

func TestSectionSpansPackets(t *testing.T) {
	sec := mkSection(TableIDPAT, 1, 0, 0, 0, patBody([2]uint16{1, 100}))
	payload := append([]byte{0x00}, sec...) // pointer_field 0 + section
	cut := 7 // split inside the section
	var a Assembler
	out, errs := a.Feed(true, payload[:cut])
	if len(out) != 0 || errs != 0 {
		t.Fatalf("first fragment: got %d sections, %d crc errors", len(out), errs)
	}
	out, errs = a.Feed(false, payload[cut:])
	if errs != 0 || len(out) != 1 {
		t.Fatalf("second fragment: got %d sections, %d crc errors", len(out), errs)
	}
	if out[0].Version != 0 || out[0].TableIDExtension != 1 {
		t.Fatalf("unexpected section: %+v", out[0])
	}
}

func TestPointerFieldCompletesPreviousSection(t *testing.T) {
	sec1 := mkSection(TableIDPAT, 1, 0, 0, 0, patBody([2]uint16{1, 100}))
	sec2 := mkSection(TableIDPAT, 1, 1, 0, 0, patBody([2]uint16{1, 110}))
	var a Assembler
	// First packet carries all but the last 3 bytes of sec1.
	head := len(sec1) - 3
	a.Feed(true, append([]byte{0x00}, sec1[:head]...))
	// Second packet: pointer_field=3 finishes sec1, then sec2 starts.
	payload := append([]byte{0x03}, sec1[head:]...)
	payload = append(payload, sec2...)
	out, errs := a.Feed(true, payload)
	if errs != 0 || len(out) != 2 {
		t.Fatalf("got %d sections, %d crc errors", len(out), errs)
	}
	if out[0].Version != 0 || out[1].Version != 1 {
		t.Fatalf("versions: got %d, %d", out[0].Version, out[1].Version)
	}
}

func TestCRCErrrorDropsSection(t *testing.T) {
	sec := mkSection(TableIDPAT, 1, 0, 0, 0, patBody([2]uint16{1, 100}))
	sec[len(sec)-1] ^= 0xFF // corrupt the CRC
	var a Assembler
	out, errs := a.Feed(true, append([]byte{0x00}, sec...))
	if len(out) != 0 || errs != 1 {
		 t.Fatalf("got %d sections, %d crc errors", len(out), errs)
	}
}

func TestStuffingAfterSection(t *testing.T) {
	sec := mkSection(TableIDPAT, 1, 0, 0, 0, patBody([2]uint16{1, 100}))
	payload := append([]byte{0x00}, sec...)
	for len(payload) < 184 {
		payload = append(payload, 0xFF)
	}
	var a Assembler
	out, errs := a.Feed(true, payload)
	if errs != 0 || len(out) != 1 {
		t.Fatalf("got %d sections, %d crc errors", len(out), errs)
	}
}

func TestOutOfOrderMultiSectionTable(t *testing.T) {
	// PAT split over two sections, delivered section 1 first.
	s0 := mkSection(TableIDPAT, 1, 0, 0, 1, patBody([2]uint16{1, 100}))
	s1 := mkSection(TableIDPAT, 1, 0, 1, 1, patBody([2]uint16{2, 200}))
	tr := NewTracker()
	if ct := tr.Add(0, mustSection(t, s1)); ct != nil {
		t.Fatal("table published before section 0 arrived")
	}
	ct := tr.Add(0, mustSection(t, s0))
	if ct == nil || !ct.NewVersion {
		t.Fatal("complete out-of-order table not published as new version")
	}
	entries, err := ParsePAT(ct)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
	if entries[0].Program != 1 || entries[1].Program != 2 {
		t.Fatalf("section order not preserved: %v", entries)
	}
}

func TestDuplicateSectionIgnored(t *testing.T) {
	s0 := mkSection(TableIDPAT, 1, 0, 0, 0, patBody([2]uint16{1, 100}))
	tr := NewTracker()
	if tr.Add(0, mustSection(t, s0)) == nil {
		t.Fatal("first publish missing")
	}
	if ct := tr.Add(0, mustSection(t, s0)); ct != nil && ct.NewVersion {
		t.Fatal("duplicate section treated as a new version")
	}
}

func TestVersionWrapAround(t *testing.T) {
	tr := NewTracker()
	s31 := mkSection(TableIDPAT, 1, 31, 0, 0, patBody([2]uint16{1, 100}))
	s0 := mkSection(TableIDPAT, 1, 0, 0, 0, patBody([2]uint16{1, 110}))
	if ct := tr.Add(0, mustSection(t, s31)); ct == nil || !ct.NewVersion {
		t.Fatal("version 31 not published")
	}
	ct := tr.Add(0, mustSection(t, s0))
	if ct == nil || !ct.NewVersion {
		t.Fatal("wrapped version 0 not treated as a new version")
	}
	if ct.Version != 0 {
		t.Fatalf("got version %d", ct.Version)
	}
}

func TestIncompleteNewVersionKeepsPublished(t *testing.T) {
	tr := NewTracker()
	v0 := mkSection(TableIDPAT, 1, 0, 0, 0, patBody([2]uint16{1, 100}))
	if tr.Add(0, mustSection(t, v0)) == nil {
		t.Fatal("v0 not published")
	}
	// v1 has two sections; only section 0 arrives.
	v1s0 := mkSection(TableIDPAT, 1, 1, 0, 1, patBody([2]uint16{1, 110}))
	if ct := tr.Add(0, mustSection(t, v1s0)); ct != nil {
		t.Fatal("incomplete v1 published")
	}
	// A v0 republish still reports the same published version.
	if ct := tr.Add(0, mustSection(t, v0)); ct == nil || ct.NewVersion {
		t.Fatal("v0 republish lost or treated as new")
	}
}

func mustSection(t *testing.T, raw []byte) Section {
	t.Helper()
	s, ok := parseSection(raw)
	if !ok {
		t.Fatal("test section failed to parse")
	}
	return s
}
