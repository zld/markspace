package publish

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStypBoxShape(t *testing.T) {
	b := stypBox()
	if len(b) < 8 || string(b[4:8]) != "styp" {
		t.Fatalf("not an styp box: % x", b)
	}
	if string(b[8:12]) != "msdh" {
		t.Fatalf("major brand = %q, want msdh", string(b[8:12]))
	}
	if !strings.Contains(string(b), "msix") || !strings.Contains(string(b), "iso6") {
		t.Fatal("compatible brands missing")
	}
}

func metasForTest() []*SegmentMeta {
	return []*SegmentMeta{
		{Path: "/tmp/a-0.m4s", Sequence: 1, BaseTime: 0, DurationTicks: 6144, SampleCount: 12, Timescale: 12288},
		{Path: "/tmp/a-1.m4s", Sequence: 2, BaseTime: 6144, DurationTicks: 6144, SampleCount: 12, Timescale: 12288},
		{Path: "/tmp/a-2.m4s", Sequence: 3, BaseTime: 12288, DurationTicks: 6144, SampleCount: 12, Timescale: 12288},
	}
}

func TestManifestContinuity(t *testing.T) {
	metas := metasForTest()
	hashes := []string{"h0", "h1", "h2"}
	m, err := BuildManifest("h264", "job", "cfg", "/tmp/init.mp4", "ih", metas, hashes)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 3 || m.Segments[2].BaseTimeTicks != 12288 {
		t.Fatalf("manifest entries wrong: %+v", m.Segments)
	}
	// A discontinuity must be rejected rather than silently written.
	bad := append([]*SegmentMeta(nil), metas...)
	bad[1] = &SegmentMeta{Path: "/tmp/x", Sequence: 2, BaseTime: 9999, DurationTicks: 1, Timescale: 12288}
	if _, err := BuildManifest("h264", "job", "cfg", "/tmp/init.mp4", "ih", bad, hashes); err == nil {
		t.Fatal("expected boundary discontinuity error")
	}
	// A non-contiguous sequence must also be rejected.
	bad2 := append([]*SegmentMeta(nil), metas...)
	bad2[1] = &SegmentMeta{Path: "/tmp/x", Sequence: 7, BaseTime: 6144, DurationTicks: 6144, Timescale: 12288}
	if _, err := BuildManifest("h264", "job", "cfg", "/tmp/init.mp4", "ih", bad2, hashes); err == nil {
		t.Fatal("expected sequence discontinuity error")
	}
}

func TestAuditTrailTimingAndRoundTrip(t *testing.T) {
	metas := metasForTest()
	trail, err := BuildAuditTrail(metas, 0x42424242, 96)
	if err != nil {
		t.Fatal(err)
	}
	if len(trail.Packets) != 3 {
		t.Fatalf("audit packets = %d, want 3", len(trail.Packets))
	}
	// 90 kHz: 6144 ticks at timescale 12288 = 0.5 s = 45000 RTP ticks.
	if trail.Packets[1].Timestamp != 45000 {
		t.Fatalf("ts = %d, want 45000", trail.Packets[1].Timestamp)
	}
	if trail.Packets[0].Sequence != 0 || trail.Packets[2].Sequence != 2 {
		t.Fatal("RTP sequence not contiguous from zero")
	}
	if err := VerifyAuditTrail(trail, metas); err != nil {
		t.Fatalf("audit verification failed: %v", err)
	}
	// Tampering a timestamp must fail verification.
	trail.Packets[1].Timestamp = 7
	if err := VerifyAuditTrail(trail, metas); err == nil {
		t.Fatal("tampered audit trail incorrectly accepted")
	}
}

func TestFixedSSRCStable(t *testing.T) {
	a := fixedSSRC("recovery-01")
	b := fixedSSRC("recovery-01")
	c := fixedSSRC("recovery-02")
	if a != b || a == 0 || a == c {
		t.Fatal("SSRC must be stable per key and distinct across keys")
	}
}

func TestManifestReadWrite(t *testing.T) {
	dir := t.TempDir()
	metas := metasForTest()
	m, err := BuildManifest("h264", "job", "cfg", filepath.Join(dir, "init.mp4"), "ih",
		metas, []string{"h0", "h1", "h2"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	if err := WriteManifest(m, path); err != nil {
		t.Fatal(err)
	}
	back, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.ConfigHash != "cfg" || len(back.Segments) != 3 {
		t.Fatal("manifest round trip lost data")
	}
}
