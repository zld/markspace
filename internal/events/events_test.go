package events

import (
	"encoding/base64"
	"strings"
	"testing"

	"medtrace/internal/core"

	"github.com/pion/rtp"
)

func TestReadParsesOrdersAndPositions(t *testing.T) {
	src := strings.Join([]string{
		`{"kind":"play","wall_unix_nanos":1704067201000000000,"track":"video","position_nanos":0,"segment":"seg-2.m4s","byte_offset":10}`,
		`{"event":"request","wall_unix_ms":100,"track":"audio","rtp_timestamp":9000,"rtp_clock_rate":48000}`,
		`{"kind":"stall","wall_rfc3339":"2024-01-01T00:00:00.000Z","track":"video","rtp_timestamp":4500,"rtp_clock_rate":90000}`,
	}, "\n")
	log, err := Read("client/client.jsonl", strings.NewReader(src))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(log.Records) != 3 {
		t.Fatalf("records = %d", len(log.Records))
	}
	// Sorted by wall clock: the 100 ms audio request first.
	if log.Records[0].Track.Kind != core.Audio {
		t.Errorf("first record kind = %q, want audio", log.Records[0].Track.Kind)
	}
	if log.Records[0].WallNanos != 100*int64(core.Millisecond) {
		t.Errorf("wall = %d", log.Records[0].WallNanos)
	}
	// The play record's explicit position and byte offset survive.
	play := log.Records[2]
	if play.PositionNanos != 0 || play.Byte != 10 || play.Segment != "client/seg-2.m4s" {
		t.Errorf("play = %+v", play)
	}
	// The stall record parsed an RTP timestamp at 90 kHz and a wall anchor.
	stall := log.Records[1]
	nanos, err := stall.RTP.Nanos()
	if err != nil {
		t.Fatal(err)
	}
	if nanos != core.Nanos(4500*1_000_000_000/90000) {
		t.Errorf("rtp nanos = %d", nanos)
	}
	if stall.RTP.ClockRate != 90000 {
		t.Errorf("clock rate = %d", stall.RTP.ClockRate)
	}
}

func TestReadRejectsAmbiguousTime(t *testing.T) {
	src := `{"kind":"play","wall_unix_nanos":1,"track":"video","position_nanos":0,"rtp_timestamp":1}`
	_, err := Read("x.jsonl", strings.NewReader(src))
	if err == nil {
		t.Fatal("expected rejection of multiple time sources")
	}
	if core.KindOf(err) != core.KindManifest {
		t.Errorf("kind = %q", core.KindOf(err))
	}
	if core.PosOf(err).Line != 1 {
		t.Errorf("line = %d", core.PosOf(err).Line)
	}
}

func TestReadRequiresWallClock(t *testing.T) {
	src := `{"kind":"play","track":"video","position_nanos":0}`
	_, err := Read("x.jsonl", strings.NewReader(src))
	if err == nil || core.KindOf(err) != core.KindManifest {
		t.Fatalf("err = %v", err)
	}
}

func TestReadMalformedLine(t *testing.T) {
	src := "{\"kind\":\"play\"\n{\"kind\":\"play\",\"wall_unix_nanos\":1,\"track\":\"video\"}"
	_, err := Read("x.jsonl", strings.NewReader(src))
	if err == nil {
		t.Fatal("expected parse error")
	}
	if core.PosOf(err).Line != 1 {
		t.Errorf("line = %d, want 1", core.PosOf(err).Line)
	}
}

func TestReadRTPPacket(t *testing.T) {
	pkt := &rtp.Packet{Header: rtp.Header{Version: 2, Timestamp: 3600, SSRC: 42}}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.StdEncoding.EncodeToString(raw)
	src := `{"kind":"play","wall_unix_nanos":1,"track":"video","rtp_packet":"` + enc + `"}`
	log, err := Read("x.jsonl", strings.NewReader(src))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if log.Records[0].RTP.Raw != 3600 || !log.Records[0].RTP.FromPacket {
		t.Errorf("rtp = %+v", log.Records[0].RTP)
	}
}

func TestTimelineWrap(t *testing.T) {
	tl := NewTimeline(90000)
	tl.Anchor(4_294_000_000, 0)
	pos, wrap, back, err := tl.Map(200)
	if err != nil {
		t.Fatal(err)
	}
	if !wrap || back {
		t.Fatalf("wrap=%v back=%v", wrap, back)
	}
	// A wrap folds in 2^32 ticks: the result must be just past the anchor.
	want := core.TicksToNanos(200+(1<<32)-4_294_000_000, 90000)
	if pos != want {
		t.Errorf("wrapped position = %d, want %d", pos, want)
	}
}

func TestTimelineBackstep(t *testing.T) {
	tl := NewTimeline(90000)
	tl.Anchor(1000, 5*core.Second)
	_, wrap, back, err := tl.Map(900)
	if err != nil {
		t.Fatal(err)
	}
	if wrap || !back {
		t.Fatalf("wrap=%v back=%v", wrap, back)
	}
}

func TestAnomaliesReportsWrap(t *testing.T) {
	src := strings.Join([]string{
		`{"kind":"a","wall_unix_nanos":1,"track":"video","rtp_timestamp":4294967000,"rtp_clock_rate":90000}`,
		`{"kind":"b","wall_unix_nanos":2,"track":"video","rtp_timestamp":200,"rtp_clock_rate":90000}`,
	}, "\n")
	log, err := Read("x.jsonl", strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	as := Anomalies(log)
	if len(as) != 1 || as[0].Reason != "timestamp-wrap" {
		t.Fatalf("anomalies = %+v", as)
	}
	if as[0].RawTimestamp != 200 || as[0].Pos.Line != 2 {
		t.Errorf("anomaly = %+v", as[0])
	}
}
