package segment

import (
	"bytes"
	"testing"
	"time"

	"github.com/pion/rtp"

	"rtpreplay/ingest"
	"rtpreplay/internal/rtplog"
	"rtpreplay/media"
)

func buildLog(t *testing.T, seqTS [][2]uint32, rate uint32) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := rtplog.WriteHeader(&buf); err != nil {
		t.Fatal(err)
	}
	base := time.Unix(1_700_000_000, 0).UTC()
	for i, st := range seqTS {
		p := rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 8,
				SequenceNumber: uint16(st[0]), Timestamp: st[1], SSRC: 1,
			},
			Payload: []byte{1, 2},
		}
		raw, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := rtplog.WriteRecord(&buf, base.Add(time.Duration(i)*20*time.Millisecond), raw); err != nil {
			t.Fatal(err)
		}
	}
	return buf.Bytes()
}

func ingestBuildStream(t *testing.T) *ingest.Stream {
	t.Helper()
	// 6 audio packets, 160 ticks (20 ms) apart.
	var seqTS [][2]uint32
	for i := 0; i < 6; i++ {
		seqTS = append(seqTS, [2]uint32{uint32(i), uint32(i * 160)})
	}
	data := buildLog(t, seqTS, media.AudioClockRate)
	st, err := ingest.ParseRTPStream(bytes.NewReader(data), int64(len(data)), media.AudioClockRate, 32)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func ingestBuildBackwardStream(t *testing.T) *ingest.Stream {
	t.Helper()
	// Video stream with a large backwards step that the unwrapper isolates.
	back := uint32(11 * 90000)
	seqTS := [][2]uint32{
		{0, 200000},
		{1, 200000 - back},
		{2, 200000 - back + 3000},
		{3, 200000 - back + 6000},
		{4, 200000 - back + 9000},
	}
	data := buildLog(t, seqTS, media.VideoClockRate)
	st, err := ingest.ParseRTPStream(bytes.NewReader(data), int64(len(data)), media.VideoClockRate, 320)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
