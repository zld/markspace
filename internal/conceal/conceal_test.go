package conceal

import (
	"testing"

	"jitterlab/internal/protocol"
)

func TestSubstituteAttenuates(t *testing.T) {
	last := &protocol.Packet{StreamID: 1, Seq: 9, Payload: []byte{100, 50, 200}}
	f := Substitute(last, 1, 10, 1, 3)
	if !f.Concealed || f.Unrecoverable {
		t.Fatalf("flags: %+v", f)
	}
	if len(f.Payload) != 3 || f.Payload[0] != 50 || f.Payload[2] != 100 {
		t.Fatalf("payload not attenuated: %v", f.Payload)
	}
	// Mutating the substitute must not touch the source frame.
	f.Payload[0] = 0
	if last.Payload[0] != 100 {
		t.Fatal("substitute aliases last-good payload")
	}
}

func TestSubstituteUnrecoverableAndEmpty(t *testing.T) {
	f := Substitute(nil, 2, 5, 6, 6)
	if !f.Unrecoverable {
		t.Fatal("run at threshold must be unrecoverable")
	}
	if len(f.Payload) != 0 {
		t.Fatal("no history must yield empty payload")
	}
}

func TestFromReal(t *testing.T) {
	p := protocol.Packet{StreamID: 3, Seq: 4, Payload: []byte{1}}
	f := FromReal(p)
	if f.Concealed || f.StreamID != 3 || f.Seq != 4 {
		t.Fatalf("%+v", f)
	}
}
