package manifest

import (
	"encoding/json"
	"strings"
	"testing"

	"continuity/internal/model"
)

func validFrame(seq uint32) model.Frame {
	return model.Frame{
		Track: 1, Sequence: seq, CaptureNanos: int64(seq) * 20_000_000,
		ContainerID: "track01.mp4", SampleRate: 48000, Channels: model.LayoutMono,
		TargetLUFS: -23, PayloadLength: 10,
		SHA256: strings.Repeat("a", 64),
	}
}

func doc(frames ...model.Frame) []byte {
	raw, _ := json.Marshal(Document{Frames: frames})
	return raw
}

func TestParseAcceptsValidManifest(t *testing.T) {
	frames, err := Parse(doc(validFrame(1), validFrame(2)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if frames[0].Number != 1 || frames[1].Number != 2 {
		t.Fatalf("frame numbers = %d,%d, want 1,2", frames[0].Number, frames[1].Number)
	}
}

func TestParseReportsFirstLocatableError(t *testing.T) {
	bad := validFrame(2)
	bad.SampleRate = 0
	_, err := Parse(doc(validFrame(1), bad, validFrame(3)))
	se, ok := model.AsStructured(err)
	if !ok {
		t.Fatalf("err = %v, want structured error", err)
	}
	if se.Stage != model.StageManifest || se.Frame != 2 {
		t.Fatalf("anchor = %s/%d, want manifest/2", se.Stage, se.Frame)
	}
}

func TestParseRejectsDuplicateSequence(t *testing.T) {
	_, err := Parse(doc(validFrame(1), validFrame(1)))
	se, ok := model.AsStructured(err)
	if !ok || se.Frame != 2 {
		t.Fatalf("duplicate not anchored to frame 2: %v", err)
	}
}

func TestParseRejectsBadChecksumFormat(t *testing.T) {
	bad := validFrame(1)
	bad.SHA256 = "XYZ"
	if _, err := Parse(doc(bad)); err == nil {
		t.Fatal("short non-hex checksum accepted")
	}
}

func TestParseRejectsZeroPayloadLength(t *testing.T) {
	bad := validFrame(1)
	bad.PayloadLength = 0
	_, err := Parse(doc(bad))
	se, ok := model.AsStructured(err)
	if !ok || se.Stage != model.StageManifest || se.Frame != 1 {
		t.Fatalf("zero payload length not rejected: %v", se)
	}
}

func TestVerifyPayloadDetectsLengthAndHashMismatch(t *testing.T) {
	f := validFrame(1)
	f.Payload = []byte("0123456789")
	f.ContainerBytes = 42
	if err := VerifyPayload(&f); err == nil {
		t.Fatal("expected hash mismatch for arbitrary payload")
	}
	// Matching length and checksum passes.
	f.SHA256 = model.Checksum(f.Payload)
	if err := VerifyPayload(&f); err != nil {
		t.Fatalf("matching payload rejected: %v", err)
	}
	// Declared length drift is caught.
	f.PayloadLength = 11
	if err := VerifyPayload(&f); err == nil {
		t.Fatal("length mismatch not detected")
	}
}
