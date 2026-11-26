package stream_test

import (
	"path/filepath"
	"testing"

	"mediasync/internal/clock"
	"mediasync/internal/fixtures"
	"mediasync/internal/issue"
	"mediasync/internal/stream"
)

// VFR: keyframe intervals must use actual decode times, not a nominal rate.
func TestVFRKeyframeIntervalsUseActualTime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vfr.mp4")
	// 30000 timescale: 1001,1001,1001,3000,1001 at samples 1,4,7,10,13
	deltas := []uint32{1001, 1001, 1001, 3000, 1001, 1001, 1001, 3000, 1001, 1001, 1001, 3000, 1001}
	sizes := make([]uint32, len(deltas))
	for i := range sizes {
		sizes[i] = 100
	}
	spec := fixtures.MP4Spec{
		Timescale: 30000, SampleDelta: deltas, SampleSizes: sizes,
		Keyframes: []int{1, 4, 7, 10, 13},
	}
	if _, err := fixtures.WriteMP4(path, spec, 1); err != nil {
		t.Fatal(err)
	}
	res, err := stream.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Keyframes) != 5 {
		t.Fatalf("keyframes=%d", len(res.Keyframes))
	}
	var gaps []clock.Tick
	for _, iv := range res.Intervals {
		gaps = append(gaps, iv.DTSGap)
	}
	// first three gaps are 3*1001/30000 s, then 3*3000/30000 s.
	// Keyframe decode times (timescale 30000): 0, 3003, 8005, 13007, 18009.
	// Gaps are 3003, 5002, 5002, 5002 -> 9009, 15006, 15006, 15006 ticks.
	expect := []clock.Tick{9009, 15006, 15006, 15006}
	for i, g := range gaps {
		if g != expect[i] {
			t.Fatalf("gap %d = %d want %d (all %v)", i, g, expect[i], gaps)
		}
	}
	if res.MedianGap != 15006 {
		t.Fatalf("median=%d", res.MedianGap)
	}
	// A nominal 30 fps assumption would give a uniform 3000 ticks; confirm we
	// did not.
	if res.MaxGap == res.MinGap {
		t.Fatalf("VFR collapse: min=max=%d", res.MinGap)
	}
}

func TestMP4TimescaleRoundingWithinOneTick(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ts.mp4")
	spec := fixtures.MP4Spec{
		Timescale: 1000, SampleDelta: []uint32{40, 40, 40, 40, 40},
		SampleSizes: []uint32{10, 10, 10, 10, 10}, Keyframes: []int{1, 3, 5},
	}
	if _, err := fixtures.WriteMP4(path, spec, 1); err != nil {
		t.Fatal(err)
	}
	res, err := stream.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	// Third keyframe at 80 ms.
	want := clock.FromMilliseconds(80)
	got := res.Keyframes[1].DTS
	if d := got - want; d < -1 || d > 1 {
		t.Fatalf("dts=%d want %d", got, want)
	}
}

func TestDuplicateAndRegressTimestamps(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dup.mp4")
	// Zero-delta sample creates a duplicate decode timestamp.
	spec := fixtures.MP4Spec{
		Timescale: 1000, SampleDelta: []uint32{40, 40, 0, 40},
		SampleSizes: []uint32{10, 10, 10, 10}, Keyframes: []int{1},
	}
	if _, err := fixtures.WriteMP4(path, spec, 1); err != nil {
		t.Fatal(err)
	}
	res, err := stream.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range res.Issues {
		if it.Category == issue.TimestampDuplicate {
			found = true
			if it.Loc.Packet != 4 {
				t.Fatalf("duplicate at packet %d", it.Loc.Packet)
			}
		}
	}
	if !found {
		t.Fatalf("no duplicate issue: %v", res.Issues)
	}
}

func TestUnsupportedStreamReported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.rtp")
	if err := writeFile(path, "not an rtp file at all"); err != nil {
		t.Fatal(err)
	}
	sess, err := stream.ParseRTPFile(path, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Issues) != 1 || sess.Issues[0].Category != issue.UnsupportedInput {
		t.Fatalf("issues=%v", sess.Issues)
	}
}

func TestNonMP4Reported(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.mp4")
	if err := writeFile(path, "definitely not mp4 bytes"); err != nil {
		t.Fatal(err)
	}
	res, err := stream.ParseMP4(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) == 0 {
		t.Fatal("no issues for non-MP4 input")
	}
}

func TestRTPWrapNearBoundary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrap.rtp")
	layout, err := fixtures.WriteRTP(path, 90000, 12, 0xFFFFFE00, 3000, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stream.ParseRTPFile(path, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Wraps != 1 {
		t.Fatalf("wraps=%d", sess.Wraps)
	}
	if len(sess.Packets) != 12 {
		t.Fatalf("packets=%d", len(sess.Packets))
	}
	// Expanded values are monotonic and the shared timeline starts at zero.
	for i := 1; i < len(sess.Packets); i++ {
		if sess.Packets[i].Arrival < sess.Packets[i-1].Arrival {
			t.Fatalf("arrival regressed at %d", i)
		}
	}
	if sess.Packets[0].Arrival != 0 {
		t.Fatalf("first arrival=%s", sess.Packets[0].Arrival)
	}
	_ = layout
}

func TestRTPTruncatedRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trunc.rtp")
	layout, err := fixtures.WriteRTP(path, 90000, 5, 0, 1000, 3, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixtures.TruncateAt(path, layout.PayloadAt[4]+5); err != nil {
		t.Fatal(err)
	}
	sess, err := stream.ParseRTPFile(path, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.Issues) == 0 {
		t.Fatal("no truncation issue")
	}
	first, _ := sess.Issues.First()
	if first.Loc.ByteOffset < 0 {
		t.Fatalf("no byte offset: %v", first)
	}
	if len(sess.Damaged) == 0 {
		t.Fatal("no damaged range")
	}
}
