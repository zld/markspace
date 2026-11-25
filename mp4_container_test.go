package mediakernel

import (
	"bytes"
	"context"
	"testing"
)

func TestMP4WrapUnwrapRoundTrip(t *testing.T) {
	payload := []byte("the framed packet stream bytes")
	raw, err := WrapInMP4(payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	got, meta, err := UnwrapMP4(raw)
	if err != nil {
		t.Fatalf("unwrap: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("mdat payload must survive encapsulation exactly")
	}
	if meta.MajorBrand != "isom" || meta.MinorVersion != 1 {
		t.Fatalf("ftyp metadata wrong: %+v", meta)
	}
	if len(meta.TopLevelBoxes) != 3 {
		t.Fatalf("want ftyp/moov/mdat, got %+v", meta.TopLevelBoxes)
	}
	wantTypes := map[string]bool{"ftyp": false, "moov": false, "mdat": false}
	for _, b := range meta.TopLevelBoxes {
		if _, ok := wantTypes[b.Type]; !ok {
			t.Fatalf("unexpected box %s", b.Type)
		}
		wantTypes[b.Type] = true
		if b.Offset < 0 || b.Size <= 0 {
			t.Fatal("box offsets/sizes must be positive and locatable")
		}
	}
	for k, found := range wantTypes {
		if !found {
			t.Fatalf("missing box %s", k)
		}
	}
	if meta.MdatPayload != len(payload) {
		t.Fatalf("mdat payload size %d != %d", meta.MdatPayload, len(payload))
	}
}

func TestMP4ContainerOnlyChangeKeepsPayload(t *testing.T) {
	payload, _ := GenerateFixture(FixtureSpec{Seed: 7, PacketCount: 8})
	v1, err := WrapInMP4(payload.Data, 1)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := WrapInMP4(payload.Data, 2)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(v1, v2) {
		t.Fatal("different ftyp minor version must change container bytes")
	}
	// Container digests differ...
	if DigestBytes(v1).Equal(DigestBytes(v2)) {
		t.Fatal("container digest must change with encapsulation")
	}
	// ...while the unwrapped payload, and thus its content digest, is identical.
	p1, _, err := UnwrapMP4(v1)
	if err != nil {
		t.Fatal(err)
	}
	p2, _, err := UnwrapMP4(v2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(p1, p2) {
		t.Fatal("container-only change must preserve mdat payload bytes")
	}
	if !DigestBytes(p1).Equal(DigestBytes(p2)) {
		t.Fatal("payload digest must be stable across container-only change")
	}
}

func TestMP4TruncationIsLocated(t *testing.T) {
	payload := []byte("payload that will be cut")
	raw, err := WrapInMP4(payload, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, cut := range []int{2, 5, 9} {
		bad := raw[:len(raw)-cut]
		_, _, err := UnwrapMP4(bad)
		if err == nil {
			t.Fatalf("cutting %d tail bytes must fail parsing", cut)
		}
		me, ok := AsMediaError(err)
		if !ok || me.Phase != PhaseParse || me.Offset < 0 || me.Reason == "" {
			t.Fatalf("truncation must carry phase/offset/reason, got %v", err)
		}
	}
}

func TestMP4RejectsGarbage(t *testing.T) {
	if _, _, err := UnwrapMP4([]byte{0, 1, 2, 3, 4, 5, 6, 7}); err == nil {
		t.Fatal("garbage bytes must not parse as MP4")
	}
	if _, _, err := UnwrapMP4(nil); err == nil {
		t.Fatal("empty input must fail")
	}
}

func TestMP4FixtureEndToEndWithPipeline(t *testing.T) {
	fx, err := GenerateFixture(FixtureSpec{Seed: 7, PacketCount: 12})
	if err != nil {
		t.Fatal(err)
	}
	container, err := WrapInMP4(fx.Data, 3)
	if err != nil {
		t.Fatal(err)
	}
	payload, _, err := UnwrapMP4(container)
	if err != nil {
		t.Fatal(err)
	}
	p := NewPipeline(RunConfig{QueueCap: 6})
	report, err := p.Process(context.Background(), payload)
	if err != nil {
		t.Fatalf("pipeline over unwrapped mp4 payload: %v", err)
	}
	if report.Counters.Packets == 0 || report.ContainerDigest.Hex == "" {
		t.Fatal("pipeline must quantify the encapsulated stream")
	}
	// The container digest covers MP4 bytes; the content digest is independent
	// of that encapsulation.
	if report.ContainerDigest.Equal(DigestBytes(container)) {
		t.Fatal("pipeline container digest is over mdat payload, not the MP4 wrapper")
	}
}
