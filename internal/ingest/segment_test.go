package ingest

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"mediapipe/internal/clock"
	"mediapipe/internal/manifest"
)

type mapSource struct {
	files map[string][]byte
	// corruptFirst makes the first read of a URI return corrupted bytes,
	// simulating a transient transfer error that a retry fixes.
	corruptFirst map[string]bool
	reads        map[string]int
}

func (m *mapSource) ReadSegment(uri string) ([]byte, error) {
	if m.reads == nil {
		m.reads = map[string]int{}
	}
	m.reads[uri]++
	data, ok := m.files[uri]
	if !ok {
		return nil, fmt.Errorf("no such segment %s", uri)
	}
	if m.corruptFirst[uri] && m.reads[uri] == 1 {
		bad := append([]byte(nil), data...)
		bad[len(bad)/2] ^= 0xff
		return bad, nil
	}
	return data, nil
}

func testRef(uri string, durSec float64) manifest.SegmentRef {
	d := clock.FromSeconds(durSec)
	return manifest.SegmentRef{TrackID: "t0", Index: 0, URI: uri, Interval: clock.Interval{Start: 0, End: d}}
}

func TestIngestOK(t *testing.T) {
	ref := testRef("a.m4s", 2)
	payload := make([]byte, int(ref.Interval.Dur())*2*2)
	src := &mapSource{files: map[string][]byte{"a.m4s": BuildSegment(payload)}}
	v := &Validator{Src: src, Channels: 2, MaxRetries: 2}
	rec, err := v.Ingest(context.Background(), ref)
	if err != nil || rec.Status != OK || rec.Attempts != 1 {
		t.Fatalf("rec=%+v err=%v", rec, err)
	}
	if len(rec.Payload) != len(payload) {
		t.Fatalf("payload %d want %d", len(rec.Payload), len(payload))
	}
}

func TestIngestMissingSlot(t *testing.T) {
	ref := testRef("", 2)
	ref.Missing = true
	v := &Validator{}
	rec, err := v.Ingest(context.Background(), ref)
	if err != nil || rec.Status != Missing {
		t.Fatalf("rec=%+v err=%v", rec, err)
	}
}

func TestIngestTruncatedUnrecoverable(t *testing.T) {
	ref := testRef("t.m4s", 2)
	payload := make([]byte, int(ref.Interval.Dur())*2*2)
	full := BuildSegment(payload)
	trunc := full[:len(full)-1000] // cut inside the payload box
	src := &mapSource{files: map[string][]byte{"t.m4s": trunc}}
	v := &Validator{Src: src, Channels: 2, MaxRetries: 2}
	rec, err := v.Ingest(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != Unrecoverable {
		t.Fatalf("status=%v reason=%q", rec.Status, rec.Reason)
	}
	if rec.Attempts != 3 { // 1 try + 2 retries, all fail
		t.Fatalf("attempts=%d", rec.Attempts)
	}
	if rec.Ref.Interval.End != ref.Interval.End {
		t.Fatal("missing interval must be preserved")
	}
}

func TestIngestChecksumRecovery(t *testing.T) {
	ref := testRef("c.m4s", 1)
	payload := make([]byte, int(ref.Interval.Dur())*2*2)
	for i := range payload {
		payload[i] = byte(i)
	}
	src := &mapSource{
		files:        map[string][]byte{"c.m4s": BuildSegment(payload)},
		corruptFirst: map[string]bool{"c.m4s": true},
	}
	v := &Validator{
		Src:        src,
		Channels:   2,
		MaxRetries: 2,
		Checksums:  map[string][32]byte{"c.m4s": sha256.Sum256(payload)},
	}
	rec, err := v.Ingest(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != Recovered || rec.Attempts != 2 {
		t.Fatalf("rec=%+v", rec)
	}
}

func TestIngestChecksumPermanentFailure(t *testing.T) {
	ref := testRef("p.m4s", 1)
	payload := make([]byte, int(ref.Interval.Dur())*2*2)
	src := &mapSource{files: map[string][]byte{"p.m4s": BuildSegment(payload)}}
	v := &Validator{
		Src:        src,
		Channels:   2,
		MaxRetries: 2,
		Checksums:  map[string][32]byte{"p.m4s": {}}, // never matches
	}
	rec, err := v.Ingest(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Status != Unrecoverable || rec.Attempts != 3 {
		t.Fatalf("rec=%+v", rec)
	}
}

func TestIngestCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ref := testRef("x.m4s", 1)
	v := &Validator{Src: &mapSource{files: map[string][]byte{}}, Channels: 2}
	_, err := v.Ingest(ctx, ref)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
}
