package buffer

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"

	"mediacore"
)

func TestRTPWrapAndDuplicate(t *testing.T) {
	p := NewRTPParser(mediacore.TrackAudio1)
	mk := func(seq uint16) []byte {
		pkt := &rtp.Packet{
			Header:  rtp.Header{SequenceNumber: seq, Timestamp: 1000},
			Payload: []byte{1, 2},
		}
		raw, err := pkt.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	// Near the 16-bit wrap point: 65534, 65535, 0, 1 with reorder.
	ev1, dup, err := p.Parse(mk(65534), 10)
	if err != nil || dup {
		t.Fatalf("first packet: dup=%v err=%v", dup, err)
	}
	ev2, _, _ := p.Parse(mk(0), 20)
	ev3, _, _ := p.Parse(mk(65535), 30) // reordered before wrap completion
	ev4, dup4, _ := p.Parse(mk(0), 40) // exact duplicate
	if p.Duplicates != 1 || !dup4 {
		t.Fatalf("duplicate accounting wrong: count=%d dup4=%v", p.Duplicates, dup4)
	}
	if !(ev1.Seq == 65534 && ev3.Seq == 65535 && ev2.Seq == 65536 && ev4.Seq == 65536) {
		t.Fatalf("extended sequence wrong: %d %d %d %d", ev1.Seq, ev3.Seq, ev2.Seq, ev4.Seq)
	}
	if p.Wrapped != 1 {
		t.Fatalf("expected one wrap, got %d", p.Wrapped)
	}
	// Corrupt packet must be a classified error.
	if _, _, err := p.Parse([]byte{0x80}, 50); err == nil {
		t.Fatal("corrupt RTP accepted")
	} else if me, ok := mediacore.AsError(err); !ok || me.Cat != mediacore.CatCorrupt {
		t.Fatalf("wrong error class: %v", err)
	}
}

func TestJitterReorderGapAndLate(t *testing.T) {
	jb := NewJitterBuffer(mediacore.TrackAudio1, 5*mediacore.Millisecond, 4, 2, 8000)
	push := func(seq uint64, recv int64) {
		late, err := jb.Push(mediacore.Event{Track: mediacore.TrackAudio1, Kind: mediacore.KindRTP,
			Seq: seq, Recv: recv, Payload: make([]byte, 4*2*2)})
		if err != nil || late {
			t.Fatalf("push seq=%d late=%v err=%v", seq, late, err)
		}
	}
	push(1, 1_000_000)
	push(3, 3_000_000) // seq 2 missing
	// Nothing released before the playout window elapses.
	if out := jb.Advance(1_000_000); len(out) != 0 {
		t.Fatalf("premature release: %d", len(out))
	}
	out := jb.Advance(9_000_000) // window (5ms) past both arrivals
	if len(out) != 3 {
		t.Fatalf("want 3 positions, got %d", len(out))
	}
	if out[0].Kind != mediacore.KindPCM || out[1].Kind != mediacore.KindGap ||
		out[2].Kind != mediacore.KindPCM || out[1].Samples != 4 {
		t.Fatalf("reorder/gap materialization wrong: %+v", out)
	}
	if jb.Gaps != 1 {
		t.Fatalf("gap count = %d", jb.Gaps)
	}
	// Seq 2 now arrives after its position was released: late, not stored.
	late, err := jb.Push(mediacore.Event{Track: mediacore.TrackAudio1, Kind: mediacore.KindRTP,
		Seq: 2, Recv: 20_000_000})
	if err != nil || !late || jb.Late != 1 {
		t.Fatalf("late handling: late=%v count=%d err=%v", late, jb.Late, err)
	}
}

func TestSilenceRecoveryAndLayoutErrors(t *testing.T) {
	rc := RecoveryConfig{Track: mediacore.TrackAudio1, Channels: 2, Rate: 8000, BlockSamples: 4}
	lr := NewLossRecovery(rc)
	block := func(seq uint64, t int64, ch int) mediacore.Event {
		return mediacore.Event{Track: mediacore.TrackAudio1, Kind: mediacore.KindPCM, Seq: seq,
			Time: t, Samples: 4, Channels: ch, Rate: 8000, Payload: make([]byte, 4*ch*2)}
	}
	dur := int64(4) * mediacore.Second / 8000
	prev, next := block(0, 0, 2), block(2, 2*dur, 2)
	gap := mediacore.Event{Track: mediacore.TrackAudio1, Kind: mediacore.KindGap, Seq: 1, Samples: 4,
		Channels: 2, Rate: 8000}
	filled, err := lr.RecoverSilence(gap, prev, next)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if filled.Time != dur || len(filled.Payload) != 4*2*2 {
		t.Fatalf("silence block placement/size wrong: %+v", filled)
	}
	for _, b := range filled.Payload {
		if b != 0 {
			t.Fatal("recovered block is not silence")
		}
	}
	// Channel-count change must be a layout error with track/stage/offset.
	bad := block(2, 2*dur, 1)
	if _, err := lr.RecoverSilence(gap, prev, bad); err == nil {
		t.Fatal("channel change accepted")
	} else if me, ok := mediacore.AsError(err); !ok || me.Cat != mediacore.CatLayout ||
		me.Track == "" || me.Stage == "" {
		t.Fatalf("layout error missing classification: %v", err)
	}
	// Payload length not divisible by channel frame.
	odd := next
	odd.Payload = []byte{1, 2, 3}
	if err := CheckContinuity(prev, odd); err == nil {
		t.Fatal("non-divisible payload accepted")
	}
	// Overlap detection.
	ov := next
	ov.Time = dur / 2
	if err := CheckContinuity(prev, ov); err == nil {
		t.Fatal("overlap accepted")
	}
}

func TestWAVParseSplitAndRoundtrip(t *testing.T) {
	rate, ch, frames := 8000, 2, 20
	pcm := make([]byte, frames*ch*2)
	for i := range pcm {
		pcm[i] = byte(i)
	}
	raw, err := WriteWAV(rate, ch, 16, pcm)
	if err != nil {
		t.Fatal(err)
	}
	rd, err := ParseWAV(mediacore.TrackAudio1, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if rd.Rate != rate || rd.Channels != ch || rd.TotalFrames != frames {
		t.Fatalf("header mismatch: %+v", rd)
	}
	blocks, err := SplitPCM(rd, 4, 0)
	if err != nil || len(blocks) != 5 {
		t.Fatalf("split: n=%d err=%v", len(blocks), err)
	}
	if blocks[1].Time != int64(4)*mediacore.Second/int64(rate) {
		t.Fatal("block timestamp not on integer scale")
	}
	// Re-serialize and parse again: byte-identical data.
	raw2, _ := WriteWAV(rate, ch, 16, rd.Data)
	rd2, err := ParseWAV(mediacore.TrackAudio1, bytes.NewReader(raw2))
	if err != nil || !bytes.Equal(rd2.Data, pcm) || rd2.TotalFrames != frames {
		t.Fatal("WAV roundtrip did not preserve samples")
	}
	// Truncated and illegal-layout samples are classified.
	for _, bad := range [][]byte{raw[:20], raw[:12], make([]byte, 44)} {
		if _, err := ParseWAV(mediacore.TrackAudio1, bytes.NewReader(bad)); err == nil {
			t.Fatal("corrupt WAV accepted")
		}
	}
}
