package rtp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pion "github.com/pion/rtp"

	"mediad/internal/adapter"
	"mediad/internal/adapter/rtp"
	_ "mediad/internal/adapter/rtp"
	"mediad/internal/fixture"
	"mediad/internal/model"
)

func writeCapture(t *testing.T, dir, name string, pkts []*pion.Packet, meta rtp.StreamMeta) string {
	t.Helper()
	data, err := fixture.WriteCapture(pkts)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	mb, err := rtp.EncodeMeta(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir,
		strings.TrimSuffix(name, filepath.Ext(name))+".meta.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRTPReconstruction(t *testing.T) {
	dir := t.TempDir()
	meta := rtp.StreamMeta{ClockRate: 8000, Kind: model.TrackAudio, FrameTicks: 160}
	var pkts []*pion.Packet
	for i := 0; i < 5; i++ {
		pkts = append(pkts, fixture.Packet(110, uint16(i), uint32(i*160), 42, false, []byte{byte(i)}))
	}
	p := writeCapture(t, dir, "stream.rtp", pkts, meta)
	tr, err := adapter.Parse(adapter.MediaInput{Path: p})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(tr.Frames) != 5 {
		t.Fatalf("frames = %d", len(tr.Frames))
	}
	// 320 ticks @ 8 kHz = 40 ms.
	if tr.Frames[2].PTS != 40_000_000 {
		t.Fatalf("pts = %d", tr.Frames[2].PTS)
	}
}

func TestRTPWrapAround(t *testing.T) {
	dir := t.TempDir()
	meta := rtp.StreamMeta{ClockRate: 8000, Kind: model.TrackAudio, FrameTicks: 160}
	near := uint32(0xFFFFFF80)
	pkts := []*pion.Packet{
		fixture.Packet(110, 0, near, 1, false, []byte{0}),
		fixture.Packet(110, 1, near+160, 1, false, []byte{1}), // wraps to 0xA0
		fixture.Packet(110, 2, near+320, 1, false, []byte{2}),
	}
	p := writeCapture(t, dir, "w.rtp", pkts, meta)
	tr, err := adapter.Parse(adapter.MediaInput{Path: p})
	if err != nil {
		t.Fatalf("wrap must recover: %v", err)
	}
	d0 := tr.Frames[1].PTS - tr.Frames[0].PTS
	d1 := tr.Frames[2].PTS - tr.Frames[1].PTS
	if d0 != 20_000_000 || d1 != 20_000_000 {
		t.Fatalf("wrap spacing = %d %d", d0, d1)
	}
}

func TestRTPIgnoresDuplicate(t *testing.T) {
	dir := t.TempDir()
	meta := rtp.StreamMeta{ClockRate: 8000, Kind: model.TrackAudio, FrameTicks: 160}
	pkts := []*pion.Packet{
		fixture.Packet(110, 0, 0, 1, false, []byte{0}),
		fixture.Packet(110, 1, 160, 1, false, []byte{1}),
		fixture.Packet(110, 1, 160, 1, false, []byte{1}), // exact duplicate
		fixture.Packet(110, 2, 320, 1, false, []byte{2}),
	}
	p := writeCapture(t, dir, "d.rtp", pkts, meta)
	tr, err := adapter.Parse(adapter.MediaInput{Path: p})
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Frames) != 3 {
		t.Fatalf("duplicate must be dropped, frames = %d", len(tr.Frames))
	}
}

func TestRTPTimestampFallback(t *testing.T) {
	dir := t.TempDir()
	meta := rtp.StreamMeta{ClockRate: 8000, Kind: model.TrackAudio, FrameTicks: 160}
	pkts := []*pion.Packet{
		fixture.Packet(110, 0, 1000, 1, false, []byte{0}),
		fixture.Packet(110, 1, 400, 1, false, []byte{1}), // backward, not a wrap
	}
	p := writeCapture(t, dir, "f.rtp", pkts, meta)
	_, err := adapter.Parse(adapter.MediaInput{Path: p})
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindTimestampFallback {
		t.Fatalf("got %v", err)
	}
	if se.Loc.Frame != 1 {
		t.Fatalf("loc frame = %d", se.Loc.Frame)
	}
}

func TestRTPUnrecoverableWrap(t *testing.T) {
	dir := t.TempDir()
	meta := rtp.StreamMeta{ClockRate: 8000, Kind: model.TrackAudio, FrameTicks: 160}
	// 0x80000000 modular jump lands exactly on the half-cycle boundary and
	// any larger jump is indistinguishable from a real fallback.
	pkts := []*pion.Packet{
		fixture.Packet(110, 0, 0, 1, false, []byte{0}),
		fixture.Packet(110, 1, 0x80000000, 1, false, []byte{1}),
	}
	p := writeCapture(t, dir, "u.rtp", pkts, meta)
	_, err := adapter.Parse(adapter.MediaInput{Path: p})
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindRTPWrapUnrecoverable {
		t.Fatalf("got %v", err)
	}
}

func TestRTPTruncatedCapture(t *testing.T) {
	dir := t.TempDir()
	meta := rtp.StreamMeta{ClockRate: 8000, Kind: model.TrackAudio, FrameTicks: 160}
	pkts := []*pion.Packet{
		fixture.Packet(110, 0, 0, 1, false, []byte{0, 1, 2, 3}),
		fixture.Packet(110, 1, 160, 1, false, []byte{4, 5, 6, 7}),
	}
	data, _ := fixture.WriteCapture(pkts)
	broken := fixture.TruncateCapture(data)
	p := filepath.Join(dir, "t.rtp")
	_ = os.WriteFile(p, broken, 0o644)
	mb, _ := rtp.EncodeMeta(meta)
	_ = os.WriteFile(filepath.Join(dir, "t.meta.json"), mb, 0o644)
	_, err := adapter.Parse(adapter.MediaInput{Path: p})
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindTruncatedFrame {
		t.Fatalf("got %v", err)
	}
}

func TestRTPZeroClock(t *testing.T) {
	dir := t.TempDir()
	meta := rtp.StreamMeta{ClockRate: 0, Kind: model.TrackAudio, FrameTicks: 160}
	pkts := []*pion.Packet{fixture.Packet(110, 0, 0, 1, false, []byte{0})}
	p := writeCapture(t, dir, "z.rtp", pkts, meta)
	_, err := adapter.Parse(adapter.MediaInput{Path: p})
	se, ok := model.AsSource(err)
	if !ok || se.Kind != model.KindZeroTimescale {
		t.Fatalf("got %v", err)
	}
}
