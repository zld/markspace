package fixture

import (
	"encoding/binary"
	"testing"
)

func TestBuildWAVStructure(t *testing.T) {
	o := WAVOptions{Channels: 2, SampleRate: 48000, Frames: 100, BitsPerSample: 16}
	buf, err := BuildWAV(o)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[0:4]) != "RIFF" || string(buf[8:12]) != "WAVE" {
		t.Fatal("missing RIFF/WAVE signature")
	}
	if string(buf[12:16]) != "fmt " {
		t.Fatal("missing fmt chunk")
	}
	le := binary.LittleEndian
	if le.Uint16(buf[22:24]) != 2 || le.Uint32(buf[24:28]) != 48000 {
		t.Fatal("fmt fields wrong")
	}
	if le.Uint16(buf[34:36]) != 16 || le.Uint16(buf[32:34]) != 4 {
		t.Fatal("block align / bits wrong")
	}
	frameSize := o.FrameSize()
	wantLen := riffHeaderLen + fmtChunkLen + dataHeaderLen + 100*frameSize
	if len(buf) != wantLen {
		t.Fatalf("size = %d, want %d", len(buf), wantLen)
	}
	// First sample frame carries the deterministic pattern.
	payloadOff := riffHeaderLen + fmtChunkLen + dataHeaderLen
	if got := le.Uint16(buf[payloadOff : payloadOff+2]); int16(got) != SampleValue(0, 0) {
		t.Fatalf("sample(0,0) = %d", got)
	}
	if got := le.Uint16(buf[payloadOff+2 : payloadOff+4]); int16(got) != SampleValue(0, 1) {
		t.Fatalf("sample(0,1) = %d", got)
	}
}

func TestBuildWAVRejectsBadOptions(t *testing.T) {
	cases := []WAVOptions{
		{Channels: 0, SampleRate: 48000, Frames: 1, BitsPerSample: 16},
		{Channels: 2, SampleRate: 0, Frames: 1, BitsPerSample: 16},
		{Channels: 2, SampleRate: 48000, Frames: 1, BitsPerSample: 12},
		{Channels: 2, SampleRate: 48000, Frames: -1, BitsPerSample: 16},
	}
	for i, c := range cases {
		if _, err := BuildWAV(c); err == nil {
			t.Fatalf("case %d must fail", i)
		}
	}
}

func TestFaultMutations(t *testing.T) {
	buf, err := BuildWAV(WAVOptions{Channels: 1, SampleRate: 8000, Frames: 10, BitsPerSample: 16})
	if err != nil {
		t.Fatal(err)
	}
	trunc, err := TruncateTail(buf, 3) // less than one 2-byte sample frame
	if err != nil {
		t.Fatal(err)
	}
	if len(trunc) != len(buf)-3 {
		t.Fatal("truncate length wrong")
	}
	bad := MutateDataLength(buf, 0xFFFFFFFF)
	dataOff := riffHeaderLen + fmtChunkLen
	if binary.LittleEndian.Uint32(bad[dataOff+4:]) != 0xFFFFFFFF {
		t.Fatal("data length mutation failed")
	}
	if _, err := TruncateTail(buf, 0); err == nil {
		t.Fatal("zero truncation must fail")
	}
}
