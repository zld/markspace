package rtpcheck_test

import (
	"testing"

	"mediapack/internal/rtpcheck"
)

func TestValidatePacketizesTS(t *testing.T) {
	ts := make([]byte, 24*188) // one generated interval
	for i := range ts {
		ts[i] = byte(i % 251)
	}
	res, err := rtpcheck.Validate(ts, 0x12345678, 100, 90000)
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if res.TSPackets != 24 {
		t.Fatalf("ts packets=%d want 24", res.TSPackets)
	}
	if res.Datagrams != 4 { // 7+7+7+3
		t.Fatalf("datagrams=%d want 4", res.Datagrams)
	}
	if res.SequenceFrom != 100 || res.SequenceTo != 103 {
		t.Fatalf("seq range %d..%d", res.SequenceFrom, res.SequenceTo)
	}
}

func TestValidateRejectsMisaligned(t *testing.T) {
	if _, err := rtpcheck.Validate(make([]byte, 100), 1, 0, 0); err == nil {
		t.Fatal("non-188 multiple must be rejected")
	}
}
