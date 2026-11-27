package scan

import (
	"os"
	"path/filepath"
	"testing"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
	"mediabatch/internal/fixture"
)

// buildSeg writes one generated fragment into dir and returns its path.
func buildSeg(t *testing.T, dir, name string, payload []byte, nonSync bool) string {
	t.Helper()
	spec := fixture.FragmentSpec{
		Sequence: 1, TrackID: 1, BaseDecode: 0, Timescale: 1000,
		Payload: payload, NonSync: nonSync,
	}
	for i := 0; i < 2; i++ {
		spec.Samples = append(spec.Samples, fixture.SampleSpec{
			Duration: 2000, Size: uint32(len(payload) / 2), Sync: i == 0,
		})
	}
	data, err := fixture.BuildFragment(spec)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func testInit(t *testing.T, dir string) string {
	t.Helper()
	data, err := fixture.BuildInit(1000, 7, "iso6")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "init.mp4")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanValidAndTruncated(t *testing.T) {
	dir := t.TempDir()
	initPath := testInit(t, dir)
	payload := make([]byte, 64)
	for i := range payload {
		payload[i] = byte(i)
	}
	good := buildSeg(t, dir, "good.m4s", payload, false)
	res := Scan(Request{Manifest: "m.mpd", Representation: "v", Ref: "good.m4s", Path: good, InitPath: initPath})
	if !res.OK() {
		t.Fatalf("status=%s issues=%+v", res.Status, res.Issues)
	}
	if res.Info.SampleCount != 2 || res.Info.TrackID != 1 {
		t.Fatalf("info=%+v", res.Info)
	}
	if !res.Info.Keyframe {
		t.Fatal("expected keyframe")
	}

	raw, _ := os.ReadFile(good)
	truncPath := filepath.Join(dir, "trunc.m4s")
	os.WriteFile(truncPath, raw[:len(raw)-9], 0o644)
	tr := Scan(Request{Manifest: "m.mpd", Ref: "trunc.m4s", Path: truncPath, InitPath: initPath})
	if tr.OK() {
		t.Fatal("truncated fragment must not pass")
	}
	if tr.Issues.Errors() == 0 {
		t.Fatalf("no error issues: %+v", tr.Issues)
	}
	for _, i := range tr.Issues {
		if i.Source.Loc.File == "" || i.Source.Segment == "" {
			t.Fatalf("issue lacks traceability: %+v", i)
		}
	}
}

func TestScanHashAndSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	good := buildSeg(t, dir, "s.m4s", make([]byte, 32), false)
	info, _ := os.Stat(good)
	exp := &Expectation{SHA256: "deadbeef", Size: info.Size()}
	res := Scan(Request{Manifest: "h.m3u8", Ref: "s.m4s", Path: good, Expected: exp})
	if res.Status != StatusHashMismatch {
		t.Fatalf("status=%s", res.Status)
	}
	if res.Issues[0].Code != fault.CodeSegmentHash {
		t.Fatalf("code=%s", res.Issues[0].Code)
	}
	exp2 := &Expectation{Size: info.Size() + 1}
	res2 := Scan(Request{Manifest: "h.m3u8", Ref: "s.m4s", Path: good, Expected: exp2})
	if res2.Status != StatusTruncated {
		t.Fatalf("status=%s", res2.Status)
	}
}

func TestScanMissingEmptyOversize(t *testing.T) {
	dir := t.TempDir()
	res := Scan(Request{Manifest: "m", Ref: "nope", Path: filepath.Join(dir, "nope")})
	if res.Status != StatusMissing {
		t.Fatalf("status=%s", res.Status)
	}
	empty := filepath.Join(dir, "empty.m4s")
	os.WriteFile(empty, nil, 0o644)
	if r := Scan(Request{Manifest: "m", Path: empty}); r.Status != StatusEmpty {
		t.Fatalf("status=%s", r.Status)
	}
	big := buildSeg(t, dir, "big.m4s", make([]byte, 128), false)
	if r := Scan(Request{Manifest: "m", Path: big, MaxBytes: 16}); r.Status != StatusOversize {
		t.Fatalf("status=%s", r.Status)
	}
}

func TestDiscontinuityBoundary(t *testing.T) {
	dir := t.TempDir()
	initPath := testInit(t, dir)
	key := buildSeg(t, dir, "key.m4s", make([]byte, 32), false)
	if r := Scan(Request{Manifest: "h", Ref: "key.m4s", Path: key, InitPath: initPath,
		Discontinuity: true, Start: clock.Zero}); !r.OK() {
		t.Fatalf("keyframe boundary should pass: %s %+v", r.Status, r.Issues)
	}
	nonKey := buildSeg(t, dir, "non.m4s", make([]byte, 32), true)
	r := Scan(Request{Manifest: "h", Ref: "non.m4s", Path: nonKey, InitPath: initPath, Discontinuity: true})
	if r.OK() {
		t.Fatal("non-keyframe at a discontinuity must be isolated")
	}
	if r.Issues[0].Code != fault.CodeDiscontinuity {
		t.Fatalf("code=%s", r.Issues[0].Code)
	}
}

func TestScanRTPPackets(t *testing.T) {
	dir := t.TempDir()
	// Build a length-prefixed RTP dump: 2-byte big-endian record length, then a
	// v2 packet with a 12-byte header and payload.
	record := func(seq uint16) []byte {
		hdr := []byte{0x80, 0x21, 0, 0, 0, 0, 0, 1, 0, 0, 0, 9}
		hdr[2] = byte(seq >> 8)
		hdr[3] = byte(seq)
		pkt := append(hdr, []byte("payload")...)
		return append([]byte{byte(len(pkt) >> 8), byte(len(pkt))}, pkt...)
	}
	data := append(record(1), record(2)...)
	p := filepath.Join(dir, "a.rtp")
	os.WriteFile(p, data, 0o644)
	res := Scan(Request{Manifest: "h", Ref: "a.rtp", Path: p, Start: clock.Zero})
	if !res.OK() {
		t.Fatalf("status=%s issues=%+v", res.Status, res.Issues)
	}
	if res.Info.Kind != "rtp" || res.Info.SampleCount != 2 {
		t.Fatalf("info=%+v", res.Info)
	}

	// Truncate mid-packet: the remaining bytes are not a valid packet.
	bad := filepath.Join(dir, "b.rtp")
	os.WriteFile(bad, data[:len(data)-3], 0o644)
	br := Scan(Request{Manifest: "h", Ref: "b.rtp", Path: bad, Start: clock.Zero})
	if br.OK() {
		t.Fatal("truncated RTP dump must fail")
	}
	found := false
	for _, i := range br.Issues {
		if i.Code == fault.CodeSegmentTruncated && i.Source.Loc.File == bad {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues=%+v", br.Issues)
	}
}
