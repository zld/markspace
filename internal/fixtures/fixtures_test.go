package fixtures

import (
	"path/filepath"
	"testing"

	"mediasync/internal/stream"
)

// TestGeneratedMP4Parses is the fixture builder's self-check: the production
// parser must see exactly the samples, keyframes and intervals written.
func TestGeneratedMP4Parses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ok.mp4")
	spec := MP4Spec{
		Timescale:   24000,
		SampleDelta: []uint32{1001, 1001, 1001, 1001, 500, 500, 1001, 1001},
		SampleSizes: []uint32{100, 120, 130, 110, 90, 95, 105, 115},
		Keyframes:   []int{1, 5, 7},
	}
	layout, err := WriteMP4(path, spec, 7)
	if err != nil {
		t.Fatal(err)
	}
	if layout.StszOffset <= layout.MdatOffset {
		t.Fatalf("unexpected layout %+v", layout)
	}
	res, err := stream.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 0 {
		t.Fatalf("unexpected issues: %v", res.Issues)
	}
	if res.Frames != 8 {
		t.Fatalf("frames=%d want 8", res.Frames)
	}
	if len(res.Keyframes) != 3 {
		t.Fatalf("keyframes=%d want 3: %+v", len(res.Keyframes), res.Keyframes)
	}
	if res.Keyframes[0].DTS != 0 {
		t.Fatalf("first dts=%s", res.Keyframes[0].DTS)
	}
	// The second keyframe is sample 5: dts = 4*1001/24000 s.
	want := "00:00:00.166" // 0.166833s truncated
	if got := res.Keyframes[1].DTS.Milliseconds(); got != 166 {
		t.Fatalf("kf2 dts=%s want ~%s", res.Keyframes[1].DTS, want)
	}
	if len(res.Intervals) != 2 {
		t.Fatalf("intervals=%d", len(res.Intervals))
	}
	if res.Intervals[0].DTSGap != res.Keyframes[1].DTS-res.Keyframes[0].DTS {
		t.Fatal("interval not derived from actual DTS")
	}
	t.Logf("kf=%+v intervals=%+v", res.Keyframes, res.Intervals)
}

func TestTruncatedSampleTableDetected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cut.mp4")
	spec := MP4Spec{
		Timescale:   25000,
		SampleDelta: []uint32{1000, 1000, 1000, 1000},
		SampleSizes: []uint32{50, 60, 70, 80},
		Keyframes:   []int{1},
	}
	layout, err := WriteMP4(path, spec, 3)
	if err != nil {
		t.Fatal(err)
	}
	// Cut inside the stsz table so the declared counts no longer fit.
	if _, err := TruncateAt(path, layout.StszOffset+14); err != nil {
		t.Fatal(err)
	}
	res, err := stream.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) == 0 {
		t.Fatal("expected truncation issues")
	}
	first, _ := res.Issues.First()
	if first.Loc.ByteOffset < 0 {
		t.Fatalf("no byte location: %v", first)
	}
	if first.Loc.ByteOffset > layout.Size {
		t.Fatalf("byte offset %d beyond original size %d", first.Loc.ByteOffset, layout.Size)
	}
	t.Logf("first=%s damaged=%v", first, res.Damaged)
}

func TestRTPFixtureRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.rtp")
	layout, err := WriteRTP(path, 90000, 10, 1000, 3000, 7, false)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stream.ParseRTPFile(path, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Packets) != 10 {
		t.Fatalf("packets=%d", len(sess.Packets))
	}
	if sess.Packets[0].Offset != layout.PayloadAt[0] {
		t.Fatalf("offset %d != %d", sess.Packets[0].Offset, layout.PayloadAt[0])
	}
	if len(sess.Issues) != 0 {
		t.Fatalf("issues: %v", sess.Issues)
	}
}

func TestRTPWrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "w.rtp")
	if _, err := WriteRTP(path, 90000, 8, 0xFFFFF000, 1000, 1, false); err != nil {
		t.Fatal(err)
	}
	sess, err := stream.ParseRTPFile(path, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Wraps != 1 {
		t.Fatalf("wraps=%d want 1: %+v", sess.Wraps, sess.Packets)
	}
	for i := 1; i < len(sess.Packets); i++ {
		if sess.Packets[i].Expanded <= sess.Packets[i-1].Expanded {
			t.Fatalf("expanded not monotonic at %d", i)
		}
	}
}

func TestRTPCorruptHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.rtp")
	layout, err := WriteRTP(path, 90000, 6, 0, 1500, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	off := layout.PayloadAt[3] // RTP version bits
	if _, err := CorruptByte(path, off, 0x40); err != nil {
		t.Fatal(err)
	}
	sess, err := stream.ParseRTPFile(path, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Issues) == 0 {
		t.Fatal("expected corrupt packet issue")
	}
	found := false
	for _, it := range sess.Issues {
		if it.Loc.ByteOffset == off {
			found = true
		}
	}
	if !found {
		t.Fatalf("no issue at corrupted offset %d: %v", off, sess.Issues)
	}
	t.Logf("issues=%v damaged=%v", sess.Issues, sess.Damaged)
}
