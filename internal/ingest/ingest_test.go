package ingest

import (
	"testing"

	"github.com/pion/rtp"

	"example.com/mediatimeline/internal/model"
)

func packet(t *testing.T, seq uint16, ts uint32, marker bool, payload []byte) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 7, Marker: marker},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestRTPReassemblesAccessUnits(t *testing.T) {
	r, err := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000, FragmentGapTicks: 1, AllowDroppedGap: true})
	if err != nil {
		t.Fatal(err)
	}
	// Three access units of two packets each at 3000-tick spacing.
	seq := uint16(1)
	for au := 0; au < 3; au++ {
		ts := uint32(au * 3000)
		if err := r.DecodeRTPPacket(packet(t, seq, ts, false, []byte("a"))); err != nil {
			t.Fatal(err)
		}
		seq++
		if err := r.DecodeRTPPacket(packet(t, seq, ts, true, []byte("b"))); err != nil {
			t.Fatal(err)
		}
		seq++
	}
	res, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(res.Samples))
	}
	if res.Samples[0].Size != 2 {
		t.Fatalf("first unit size = %d, want 2", res.Samples[0].Size)
	}
	if got := res.Samples[1].Time.RepairedTime; got.Num != 1 || got.Den != 30 {
		t.Fatalf("second unit time = %+v, want 1/30", got)
	}
}

func TestRTPWrapExtended(t *testing.T) {
	r, _ := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000, FragmentGapTicks: 1})
	// First packet just before the wrap, second just after.
	if err := r.Push(PacketObservation{Seq: 1, RawTicks: int64(1<<32) - 1500, Marker: true, Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(PacketObservation{Seq: 2, RawTicks: 1500, Marker: true, Payload: []byte("y")}); err != nil {
		t.Fatal(err)
	}
	res, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Samples) != 2 {
		t.Fatalf("samples = %d", len(res.Samples))
	}
	var sawWrap bool
	for _, d := range res.Decisions {
		if d.Kind == "wrap-extended" {
			sawWrap = true
		}
	}
	if !sawWrap {
		t.Fatal("wrap decision not recorded")
	}
	if res.Samples[1].Time.Epoch != 1 {
		t.Fatalf("second sample epoch = %d, want 1", res.Samples[1].Time.Epoch)
	}
}

func TestRTPLossAndReorderRecorded(t *testing.T) {
	r, _ := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000, FragmentGapTicks: 1, AllowDroppedGap: true})
	// Sequences 1,2 then a jump to 6 (3 packets lost), then 5 (reordered).
	for _, seq := range []uint16{1, 2, 6, 5} {
		if err := r.Push(PacketObservation{Seq: seq, RawTicks: int64(seq) * 3000, Marker: true, Payload: []byte("z")}); err != nil {
			t.Fatalf("seq %d: %v", seq, err)
		}
	}
	res, err := r.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if res.LostPackets != 3 {
		t.Fatalf("lost = %d, want 3", res.LostPackets)
	}
	if res.Reordered != 1 {
		t.Fatalf("reordered = %d, want 1", res.Reordered)
	}
	var sawGap bool
	for _, e := range res.Errors {
		if e.Code == model.CodeMissingPayload && e.Recoverable {
			sawGap = true
		}
	}
	if !sawGap {
		t.Fatalf("recoverable gap error not recorded: %v", res.Errors)
	}
	// Samples are ordered by repaired time: the late packet (15000 ticks =
	// 1/6 s) places before the 18000-tick packet (1/5 s) that arrived ahead.
	if got, want := res.Samples[2].Time.RepairedTime, (model.RationalTime{Num: 1, Den: 6}); got != want {
		t.Fatalf("late sample time = %+v, want %+v", got, want)
	}
	if got, want := res.Samples[3].Time.RepairedTime, (model.RationalTime{Num: 1, Den: 5}); got != want {
		t.Fatalf("sample after late packet = %+v, want %+v", got, want)
	}
}

func TestRTPBackwardUnrecoverable(t *testing.T) {
	r, _ := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000, FragmentGapTicks: 1, Jitter: 3000})
	for i := 0; i < 4; i++ {
		if err := r.Push(PacketObservation{Seq: uint16(i), RawTicks: int64(i) * 3000, Marker: true, Payload: []byte("p")}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Push(PacketObservation{Seq: 4, RawTicks: 1000, Marker: true, Payload: []byte("q")}); err != nil {
		t.Fatalf("push must defer clock faults until Finish: %v", err)
	}
	_, err := r.Finish()
	if err == nil {
		t.Fatal("expected an unrecoverable backward error")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Code != model.CodeBackwardTimestamp || pe.Stage != model.StageClock || pe.Recoverable {
		t.Fatalf("unexpected classified error: %v", err)
	}
}

func TestRTPMalformedPacketClassified(t *testing.T) {
	r, _ := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000})
	err := r.DecodeRTPPacket([]byte{0x80})
	if err == nil {
		t.Fatal("expected a parse error")
	}
	pe, ok := model.AsError(err)
	if !ok || pe.Stage != model.StageParse || !pe.Recoverable {
		t.Fatalf("malformed packet should be a recoverable parse error: %v", err)
	}
	res, ferr := r.Finish()
	if ferr == nil {
		t.Fatalf("finish should fail without packets: %v", res)
	}
}

func TestRTPEmptyPayloadRecorded(t *testing.T) {
	r, _ := NewRTPReassembler(RTPOptions{Track: model.TrackVideoMain, Timescale: 90000, FragmentGapTicks: 1})
	if err := r.Push(PacketObservation{Seq: 1, RawTicks: 0, Marker: true, Payload: nil}); err != nil {
		t.Fatal(err)
	}
	res, err := r.Finish()
	if err == nil {
		t.Fatal("expected failure with no usable samples")
	}
	if len(res.Errors) == 0 || res.Errors[0].Code != model.CodeMissingPayload {
		t.Fatalf("expected missing-payload error, got %v", res.Errors)
	}
}
