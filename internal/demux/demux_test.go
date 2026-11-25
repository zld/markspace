package demux_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/rtp"

	"mediacore/internal/demux"
	"mediacore/internal/model"
	"mediacore/internal/remux"
)

// writeTinyMP4 builds a minimal one-track MP4 via the remux writer.
func writeTinyMP4(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "tiny.mp4")
	outs := []model.OutputTrack{
		{ID: "out:v", Kind: model.TrackVideo, Timescale: 90000, Accepts: []model.EventKind{model.EventMedia, model.EventSEI}},
	}
	sei := append([]byte{0x06, 0x05, 0x02, 0xAA, 0xBB}, 0x80)
	nal := make([]byte, 4+len(sei))
	binary.BigEndian.PutUint32(nal, uint32(len(sei)))
	copy(nal[4:], sei)
	events := []model.Event{
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 0, Duration: 3000, Size: uint32(len(nal)), OutTrackID: "out:v", Payload: nal},
		{TrackID: "mp4:1", Kind: model.EventSEI, Ticks: 0, SrcOffset: 4, Size: uint32(len(sei)), OutTrackID: "out:v", Payload: sei},
		{TrackID: "mp4:1", Kind: model.EventMedia, Ticks: 3000, Duration: 3000, Size: 4, OutTrackID: "out:v", Payload: []byte{0, 0, 0, 0}},
	}
	if err := remux.WriteMP4(path, outs, events); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDemuxMP4MediaAndSEI(t *testing.T) {
	dir := t.TempDir()
	path := writeTinyMP4(t, dir)
	tracks, events, err := demux.DemuxMP4(path, demux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || tracks[0].Kind != model.TrackVideo || tracks[0].Timescale != 90000 {
		t.Fatalf("tracks: %+v", tracks)
	}
	var media, sei int
	for _, e := range events {
		switch e.Kind {
		case model.EventMedia:
			media++
		case model.EventSEI:
			sei++
			if e.Payload[0]&0x1f != 6 {
				t.Fatalf("SEI payload type: %x", e.Payload[0])
			}
		}
	}
	if media != 2 || sei != 1 {
		t.Fatalf("media=%d sei=%d, want 2/1", media, sei)
	}
}

func TestDemuxTruncated(t *testing.T) {
	dir := t.TempDir()
	path := writeTinyMP4(t, dir)
	raw, _ := os.ReadFile(path)
	short := filepath.Join(dir, "trunc.mp4")
	if err := os.WriteFile(short, raw[:len(raw)-7], 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := demux.DemuxMP4(short, demux.Options{})
	me, ok := err.(*model.Error)
	if !ok || me.Category != model.ErrTruncated || me.Stage != model.StageDemux {
		t.Fatalf("want truncated demux error, got %v", err)
	}
	if me.Offset < 0 {
		t.Fatalf("offset not recorded: %+v", me)
	}
}

func TestDemuxBadLength(t *testing.T) {
	dir := t.TempDir()
	path := writeTinyMP4(t, dir)
	raw, _ := os.ReadFile(path)
	// Corrupt the ftyp box size to an absurd value.
	binary.BigEndian.PutUint32(raw[0:4], 0x7fffffff)
	bad := filepath.Join(dir, "badlen.mp4")
	if err := os.WriteFile(bad, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := demux.DemuxMP4(bad, demux.Options{})
	me, ok := err.(*model.Error)
	if !ok || (me.Category != model.ErrBadLength && me.Category != model.ErrTruncated) {
		t.Fatalf("want bad_length/truncated, got %v", err)
	}
	if me.Offset != 0 {
		t.Fatalf("offset = %d, want 0", me.Offset)
	}
}

func TestDemuxRTPWrap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrap.rtp")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// Sequence and timestamp both cross their wrap points.
	seqs := []uint16{65534, 65535, 0, 1}
	tss := []uint32{0xfffffffc, 0xffffffff, 2, 5}
	for i := range seqs {
		pkt := rtp.Packet{
			Header:  rtp.Header{Version: 2, SequenceNumber: seqs[i], Timestamp: tss[i], PayloadType: 96},
			Payload: []byte{byte(i), 0xde, 0xad},
		}
		raw, _ := pkt.Marshal()
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], uint16(len(raw)))
		f.Write(lb[:])
		f.Write(raw)
	}
	f.Close()
	tracks, events, err := demux.DemuxRTP(path, demux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 1 || len(events) != 4 {
		t.Fatalf("tracks=%d events=%d", len(tracks), len(events))
	}
	// Extended timestamps must be monotonic across the wrap.
	for i := 1; i < len(events); i++ {
		if events[i].Ticks <= events[i-1].Ticks {
			t.Fatalf("ticks not monotonic at %d: %d <= %d", i, events[i].Ticks, events[i-1].Ticks)
		}
	}
	if events[3].Ticks != 0x100000005 {
		t.Fatalf("last extended ts = %d, want %d", events[3].Ticks, 0x100000005)
	}
}

func TestDemuxRTPTruncated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.rtp")
	if err := os.WriteFile(path, []byte{0, 20, 1, 2, 3}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := demux.DemuxRTP(path, demux.Options{})
	me, ok := err.(*model.Error)
	if !ok || me.Category != model.ErrTruncated {
		t.Fatalf("want truncated, got %v", err)
	}
}
