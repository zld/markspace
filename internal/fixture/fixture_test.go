package fixture

import (
	"testing"

	"regress/internal/model"
)

func TestPlanDeterministicAndFaults(t *testing.T) {
	p1, err := Plan(DefaultSpec(FaultNone))
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Plan(DefaultSpec(FaultNone))
	if err != nil {
		t.Fatal(err)
	}
	if len(p1) != len(p2) {
		t.Fatalf("plan length unstable: %d vs %d", len(p1), len(p2))
	}
	var drops, dups int
	keys := map[string]bool{}
	for i, it := range p1 {
		if it.Key != p2[i].Key || it.ExpectedTick != p2[i].ExpectedTick {
			t.Fatalf("plan not deterministic at %d", i)
		}
		if keys[it.Key] {
			t.Fatalf("duplicate key in plan itself: %s", it.Key)
		}
		keys[it.Key] = true
		if it.Drop {
			drops++
		}
		if it.Duplicate {
			dups++
		}
	}
	if drops != 1 || dups != 1 {
		t.Fatalf("want 1 drop and 1 duplicate, got %d/%d", drops, dups)
	}
}

func TestBuildMP4RoundTripsShape(t *testing.T) {
	plan, _ := Plan(DefaultSpec(FaultNone))
	data, cut, err := BuildMP4(DefaultSpec(FaultNone), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || cut != 0 {
		t.Fatalf("normal fixture: size=%d cut=%d", len(data), cut)
	}

	truncPlan, _ := Plan(DefaultSpec(FaultTruncate))
	tdata, tcut, err := BuildMP4(DefaultSpec(FaultTruncate), truncPlan)
	if err != nil {
		t.Fatal(err)
	}
	if tcut <= 0 || tcut >= int64(len(tdata)) {
		t.Fatalf("bad cut offset: %d size=%d", tcut, len(tdata))
	}
}

func TestBuildRTPWrapsAndTruncates(t *testing.T) {
	plan, _ := Plan(DefaultSpec(FaultWrap))
	cap, err := BuildRTP(DefaultSpec(FaultWrap), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(cap.Packets) == 0 {
		t.Fatal("no packets generated")
	}
	// Sequence numbers must walk 0..n-1 (16-bit wraparound handled by uint16).
	for i, pkt := range cap.Packets {
		if pkt.SequenceNumber != uint16(i) {
			t.Fatalf("seq mismatch at %d: %d", i, pkt.SequenceNumber)
		}
	}
	// Video stream starts near the 32-bit boundary: confirm at least one
	// timestamp wrap occurs between consecutive video packets.
	wrapped := false
	var prev uint32
	first := true
	for _, pkt := range cap.Packets {
		if pkt.PayloadType != PTVideoHigh {
			continue
		}
		if !first && pkt.Timestamp < prev {
			wrapped = true
		}
		prev, first = pkt.Timestamp, false
	}
	if !wrapped {
		t.Fatal("expected a 32-bit timestamp wrap in video stream")
	}
	raw, err := cap.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	tcap, _ := BuildRTP(DefaultSpec(FaultTruncate), plan)
	if tcap.TruncatedByte <= 0 || tcap.TruncatedByte >= int64(len(raw)) {
		t.Fatalf("bad truncation offset: %d (file %d bytes)", tcap.TruncatedByte, len(raw))
	}
}

func TestPlanCoversAllMetadataKinds(t *testing.T) {
	plan, _ := Plan(DefaultSpec(FaultNone))
	seen := map[model.TrackKind]bool{}
	for _, it := range plan {
		seen[it.Kind] = true
	}
	for _, k := range []model.TrackKind{model.KindID3, model.KindEmsg, model.KindSEI,
		model.KindSubtitle, model.KindChapter} {
		if !seen[k] {
			t.Errorf("plan missing metadata kind %s", k)
		}
	}
}
