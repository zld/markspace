package mp4

import (
	"os"
	"path/filepath"
	"testing"

	gomp4 "github.com/abema/go-mp4"

	"rtp-rebuild/internal/clock"
	"rtp-rebuild/internal/rtp"
)

func packets(n int, step clock.Tick) []rtp.Packet {
	out := make([]rtp.Packet, n)
	for i := range out {
		out[i] = rtp.Packet{
			SSRC: 1, Seq: uint16(i), ExtSeq: int64(i),
			TS: clock.Tick(i) * step, RawTS: uint32(i) * uint32(step),
			Payload: []byte("sample-payload"),
			Marker:  i == n-1,
		}
	}
	return out
}

func TestWriteTrackStructure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.mp4")
	track, err := WriteTrack(path, packets(10, 3000), WriteOptions{})
	if err != nil {
		t.Fatalf("WriteTrack: %v", err)
	}
	if len(track.Samples) != 10 {
		t.Fatalf("samples = %d", len(track.Samples))
	}
	if track.Duration != 30000 {
		t.Fatalf("duration = %d, want 30000", track.Duration)
	}
	if track.Timescale != clock.TickRate {
		t.Fatalf("timescale = %d", track.Timescale)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var boxes []string
	var sizes []uint64
	if _, err := gomp4.ReadBoxStructure(f, func(h *gomp4.ReadHandle) (interface{}, error) {
		if len(h.Path) == 1 {
			boxes = append(boxes, h.BoxInfo.Type.String())
			sizes = append(sizes, h.BoxInfo.Size)
		}
		return h.Expand()
	}); err != nil {
		t.Fatalf("ReadBoxStructure: %v", err)
	}
	if len(boxes) < 3 || boxes[0] != "ftyp" || boxes[1] != "moov" || boxes[2] != "mdat" {
		t.Fatalf("top level boxes = %v", boxes)
	}
	// The mdat size must cover every sample plus its own header.
	var payload int64
	for _, p := range packets(10, 3000) {
		payload += int64(len(p.Payload))
	}
	if sizes[2] != uint64(payload+8) {
		t.Fatalf("mdat size = %d, want %d", sizes[2], payload+8)
	}
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != track.Bytes {
		t.Fatalf("file size = %d, track bytes = %d", info.Size(), track.Bytes)
	}
}

func TestChunkOffsetsPointAtSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.mp4")
	track, err := WriteTrack(path, packets(4, 3000), WriteOptions{})
	if err != nil {
		t.Fatalf("WriteTrack: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range track.Samples {
		got := data[s.Offset : s.Offset+s.Size]
		want := []byte("sample-payload")
		if string(got) != string(want) {
			t.Fatalf("sample %d bytes = %q, want %q", i, got, want)
		}
	}
}

func TestDurationsFromDeltas(t *testing.T) {
	pkts := []rtp.Packet{
		{TS: 0}, {TS: 3000}, {TS: 6000}, {TS: 12000},
	}
	got := durationsFor(pkts, clock.TickRate)
	want := []uint32{3000, 3000, 6000, 6000}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("durations = %v, want %v", got, want)
		}
	}
}

func TestDurationsHandleNonMonotonic(t *testing.T) {
	pkts := []rtp.Packet{{TS: 0}, {TS: 3000}, {TS: 1000}, {TS: 4000}}
	got := durationsFor(pkts, clock.TickRate)
	for i, d := range got {
		if d == 0 {
			t.Fatalf("duration %d is zero", i)
		}
	}
}

func TestDurationsSkipSuspect(t *testing.T) {
	pkts := []rtp.Packet{{TS: 0}, {TS: 0, TSSuspect: true}, {TS: 6000}}
	got := durationsFor(pkts, clock.TickRate)
	if got[0] == 0 || got[1] == 0 {
		t.Fatalf("durations = %v", got)
	}
}

func TestEmptyTrackRejected(t *testing.T) {
	_, err := WriteTrack(filepath.Join(t.TempDir(), "x.mp4"), nil, WriteOptions{})
	if err == nil {
		t.Fatal("empty track should fail")
	}
}

func TestSingleSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one.mp4")
	track, err := WriteTrack(path, packets(1, 3000), WriteOptions{})
	if err != nil {
		t.Fatalf("WriteTrack: %v", err)
	}
	if len(track.Samples) != 1 || track.Duration != 1 {
		t.Fatalf("track = %+v", track)
	}
}
