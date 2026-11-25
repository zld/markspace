package fingerprint

import (
	"errors"
	"hash/crc32"
	"testing"

	"github.com/zld/mediacore/internal/trace"
)

func TestBytesStableAndLengthFramed(t *testing.T) {
	if Bytes([]byte("ab"), []byte("c")) == Bytes([]byte("a"), []byte("bc")) {
		t.Fatal("length framing must distinguish chunk boundaries")
	}
	if Bytes([]byte("x")) != Bytes([]byte("x")) {
		t.Fatal("digest not stable")
	}
}

func TestMarkDuplicatesOnlyReportsRelation(t *testing.T) {
	records := []Record{
		{Key: "a", Digest: "same"}, {Key: "b", Digest: "same"}, {Key: "c", Digest: "other"},
	}
	groups := MarkDuplicates(records)
	if len(groups) != 1 || groups[0].Digest != "same" || len(groups[0].Keys) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
}

func TestVerifyCRCPropagatesLocatedFingerprintError(t *testing.T) {
	payload := []byte("important media payload")
	expected := crc32.ChecksumIEEE(payload)
	payload[0] ^= 0xFF
	err := VerifyCRC(expected, payload, 4, 1234)
	var te *trace.Error
	if !errors.As(err, &te) || te.Code != trace.CodePayloadMismatch || te.Stage != trace.StageFingerprint {
		t.Fatalf("unexpected error: %v", err)
	}
	if !te.HasTrack || te.TrackID != 4 || !te.HasByteOffset || te.ByteOffset != 1234 {
		t.Fatalf("location = %+v", te)
	}
}
