package digest

import "testing"

func TestHasherTwoTier(t *testing.T) {
	h := NewHasher()
	raw := make([]byte, 188)
	raw[0] = 0x47
	h.AddPacket(raw, raw[4:])
	h.AddPacket(raw, raw[4:])
	d := h.Sum()
	if h.Packets() != 2 || h.RawBytes() != 376 {
		t.Fatalf("counters wrong: packets=%d bytes=%d", h.Packets(), h.RawBytes())
	}
	r1, c1 := d.Hex()
	if len(r1) != 64 || len(c1) != 64 {
		t.Fatalf("hex digest length wrong: %q %q", r1, c1)
	}
	// Nil payload contributes nothing to the content hash.
	hNil := NewHasher()
	hNil.AddPacket(raw, nil)
	if hNil.Sum().Content != NewHasher().Sum().Content {
		t.Fatal("nil payload must leave the content hash at its empty value")
	}
}

func TestHasherDeterministic(t *testing.T) {
	mk := func() Digests {
		h := NewHasher()
		for i := byte(0); i < 5; i++ {
			p := make([]byte, 188)
			p[0] = 0x47
			p[10] = i
			h.AddPacket(p, p[4:])
		}
		return h.Sum()
	}
	if mk() != mk() {
		t.Fatal("identical input must yield identical digests")
	}
}

func TestClassifyDistinguishesChangeKinds(t *testing.T) {
	// Baseline: one packet with a header byte and a payload.
	base := func(header, payload byte) Digests {
		h := NewHasher()
		p := make([]byte, 188)
		p[0] = 0x47
		p[3] = header  // transport header region (CC)
		p[50] = payload // demuxed payload region
		h.AddPacket(p, p[4:])
		return h.Sum()
	}
	stored := base(0x00, 0x77)

	// Identical input => unchanged.
	if got := base(0x00, 0x77).Classify(stored); got != ChangeUnchanged {
		t.Fatalf("identical input classified as %v", got)
	}
	// Header-only change (CC rewrite) with same payload => encapsulation only.
	encap := base(0x05, 0x77)
	if got := encap.Classify(stored); got != ChangeEncapsulationOnly {
		t.Fatalf("remux classified as %v, want encapsulation-only", got)
	}
	// Payload change => real content change requiring dependent recompute.
	content := base(0x00, 0x99)
	if got := content.Classify(stored); got != ChangeContent {
		t.Fatalf("payload edit classified as %v, want content", got)
	}
}
