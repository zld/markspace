package rtp

import (
	"testing"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
)

func TestSequenceDamageCarriesExactOffset(t *testing.T) {
	p := fixture.BuildRTP()
	if p.Damage == nil {
		t.Fatal("fixture has no damage mark")
	}
	stream, err := ParseStream(p.Data, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Framed || len(stream.Packets) != 4 {
		t.Fatalf("framed=%v packets=%d", stream.Framed, len(stream.Packets))
	}
	if len(stream.Damage) != 1 {
		t.Fatalf("damage: %+v", stream.Damage)
	}
	d := stream.Damage[0]
	if d.Offset != p.Damage.Offset || d.Expected != 102 || d.Got != 103 || d.Missing != 1 {
		t.Fatalf("damage: %+v want offset %d", d, p.Damage.Offset)
	}
}

func TestIncompleteHeaderKeepsVerifiedPrefix(t *testing.T) {
	p := fixture.BuildRTP()
	cut := p.Data[:len(p.Data)-6]
	stream, err := ParseStream(cut, Options{})
	if err == nil {
		t.Fatal("expected incomplete error")
	}
	if _, ok := err.(*ErrIncomplete); !ok {
		t.Fatalf("wrong error type: %v", err)
	}
	if len(stream.Packets) != 3 {
		t.Fatalf("verified prefix: %d packets", len(stream.Packets))
	}
}

func TestUnknownLayoutFails(t *testing.T) {
	_, err := ParseStream([]byte{1, 2, 3, 4, 5}, Options{})
	if err == nil {
		t.Fatal("expected framing error")
	}
	if fe, ok := fault.As(err); !ok || fe.Stage != fault.StageRTP {
		t.Fatalf("wrong error: %v", err)
	}
}
