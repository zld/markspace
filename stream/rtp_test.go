package stream

import (
	"testing"

	"mediad/media"

	"github.com/pion/rtp"
)

func marshalPacket(t *testing.T, seq uint16, stamp uint32, payload []byte) []byte {
	t.Helper()
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      stamp,
			SSRC:           0x11223344,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("marshal RTP: %v", err)
	}
	return raw
}

func TestDecodeInOrder(t *testing.T) {
	tb := media.TimeBase{SampleRate: 48000, RTPClock: 48000}
	d := NewDecoder(tb)
	raw := [][]byte{
		marshalPacket(t, 0, 0, []byte{1, 2}),
		marshalPacket(t, 1, 960, []byte{3, 4}),
		marshalPacket(t, 2, 1920, []byte{5, 6}),
	}
	res := d.Decode(raw)
	if len(res.Faults) != 0 {
		t.Fatalf("in-order stream must be clean: %+v", res.Faults)
	}
	if len(res.Packets) != 3 {
		t.Fatalf("got %d packets, want 3", len(res.Packets))
	}
	if res.Packets[2].ExtendedSequence != 2 || res.Packets[2].SampleOffset != 1920 {
		t.Fatalf("packet 2 wrong: %+v", res.Packets[2])
	}
	if res.HighestExtendedSequence != 2 {
		t.Fatalf("highest = %d, want 2", res.HighestExtendedSequence)
	}
}

func TestDecodeSequenceWrap(t *testing.T) {
	tb := media.TimeBase{SampleRate: 48000, RTPClock: 48000}
	d := NewDecoder(tb)
	raw := [][]byte{
		marshalPacket(t, 65534, 100, nil),
		marshalPacket(t, 65535, 200, nil),
		marshalPacket(t, 0, 300, nil),
		marshalPacket(t, 1, 400, nil),
	}
	res := d.Decode(raw)
	if res.Packets[2].ExtendedSequence != 65536 {
		t.Fatalf("wrapped extended seq = %d, want 65536", res.Packets[2].ExtendedSequence)
	}
	if res.Packets[3].ExtendedSequence != 65537 {
		t.Fatalf("post-wrap extended seq = %d, want 65537", res.Packets[3].ExtendedSequence)
	}
	hasWrap := false
	for _, flt := range res.Faults {
		if flt.Category == media.CatRTPSeqWrap {
			hasWrap = true
		}
	}
	if !hasWrap {
		t.Fatalf("sequence wrap not reported: %+v", res.Faults)
	}
	// Wrap markers are informational and must not fail the quality gate.
	if media.HasFatal(res.Faults) {
		t.Fatalf("a clean wrap must not be quality-fatal: %+v", res.Faults)
	}
}

func TestDecodeTimestampWrap(t *testing.T) {
	// RTP clock 8000, sample rate 8000 so ticks map 1:1.
	tb := media.TimeBase{SampleRate: 8000, RTPClock: 8000}
	d := NewDecoder(tb)
	raw := [][]byte{
		marshalPacket(t, 10, 0xFFFFFFF0, nil),
		marshalPacket(t, 11, 0x00000010, nil),
	}
	res := d.Decode(raw)
	hasWrap := false
	for _, flt := range res.Faults {
		if flt.Category == media.CatRTPTSWrap {
			hasWrap = true
		}
	}
	if !hasWrap {
		t.Fatalf("timestamp wrap not reported: %+v", res.Faults)
	}
	// 0x100000010 ticks at 1:1 mapping.
	if res.Packets[1].ExtendedStamp != 0x100000010 {
		t.Fatalf("extended stamp = %d, want 4294967312", res.Packets[1].ExtendedStamp)
	}
	if res.Packets[1].SampleOffset != 0x100000010 {
		t.Fatalf("sample offset = %d, want 4294967312", res.Packets[1].SampleOffset)
	}
}

func TestDecodeReorder(t *testing.T) {
	tb := media.TimeBase{SampleRate: 48000, RTPClock: 48000}
	d := NewDecoder(tb)
	raw := [][]byte{
		marshalPacket(t, 100, 1000, nil),
		marshalPacket(t, 102, 3000, nil),
		marshalPacket(t, 101, 2000, nil), // late arrival
	}
	res := d.Decode(raw)
	var reorder *media.Fault
	for i := range res.Faults {
		if res.Faults[i].Category == media.CatRTPReorder {
			reorder = &res.Faults[i]
		}
	}
	if reorder == nil {
		t.Fatalf("reorder not detected: %+v", res.Faults)
	}
	if res.HighestExtendedSequence != 102 {
		t.Fatalf("highest = %d, want 102", res.HighestExtendedSequence)
	}
	if !media.QualityFatal(media.CatRTPReorder) {
		t.Fatal("reorder must be quality-fatal")
	}
}

func TestDecodeClockUnavailable(t *testing.T) {
	// No RTP clock: conversion must fail loudly, not return zero silently.
	tb := media.TimeBase{SampleRate: 48000, RTPClock: 0}
	d := NewDecoder(tb)
	res := d.Decode([][]byte{marshalPacket(t, 0, 1234, nil)})
	found := false
	for _, flt := range res.Faults {
		if flt.Category == media.CatRTPClockUnavailable {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing clock must be reported: %+v", res.Faults)
	}
	if res.Packets[0].SampleOffset != 0 {
		t.Fatal("unconvertible offset must stay zero")
	}
}

func TestDecodeInexactClock(t *testing.T) {
	// 44.1k PCM against a 48k RTP clock: 1 tick does not map to whole samples.
	tb := media.TimeBase{SampleRate: 44100, RTPClock: 48000}
	d := NewDecoder(tb)
	res := d.Decode([][]byte{
		marshalPacket(t, 0, 0, nil),
		marshalPacket(t, 1, 1, nil),
	})
	if len(res.Packets) != 2 {
		t.Fatalf("packets still decoded: %d", len(res.Packets))
	}
	if !media.HasFatal(res.Faults) {
		t.Fatalf("inexact conversion must be fatal, not silently truncated: %+v", res.Faults)
	}
}

func TestDecodeMalformedPacket(t *testing.T) {
	tb := media.TimeBase{SampleRate: 48000, RTPClock: 48000}
	d := NewDecoder(tb)
	good := marshalPacket(t, 5, 50, []byte{9})
	res := d.Decode([][]byte{{0x80}, good}) // too short to contain a header
	var mal *media.Fault
	for i := range res.Faults {
		if res.Faults[i].Category == media.CatRTPMalformed {
			mal = &res.Faults[i]
		}
	}
	if mal == nil {
		t.Fatalf("malformed packet not reported: %+v", res.Faults)
	}
	if len(res.Packets) != 1 || res.Packets[0].Sequence != 5 {
		t.Fatalf("valid packet after malformed must still decode: %+v", res.Packets)
	}
}

func TestDecodeCrossClockScaling(t *testing.T) {
	// 8kHz PCM carried on a 48kHz RTP clock: 48000 ticks = 8000 samples.
	tb := media.TimeBase{SampleRate: 8000, RTPClock: 48000}
	d := NewDecoder(tb)
	res := d.Decode([][]byte{
		marshalPacket(t, 0, 0, nil),
		marshalPacket(t, 1, 48000, nil),
	})
	if len(res.Faults) != 0 {
		t.Fatalf("exact cross-clock scaling must be clean: %+v", res.Faults)
	}
	if res.Packets[1].SampleOffset != 8000 {
		t.Fatalf("scaled offset = %d, want 8000", res.Packets[1].SampleOffset)
	}
}
