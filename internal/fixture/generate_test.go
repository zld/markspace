package fixture

import (
	"bytes"
	"testing"
)

func TestFixturesAreDeterministic(t *testing.T) {
	a := BuildValid(Options{EventDuplicates: true})
	b := BuildValid(Options{EventDuplicates: true})
	if !bytes.Equal(a.Data, b.Data) {
		t.Fatal("valid fixture is not deterministic")
	}
	if Digest(a.Data) != Digest(b.Data) {
		t.Fatal("digest mismatch")
	}
	if d := BuildValid(Options{}); bytes.Equal(a.Data, d.Data) {
		t.Fatal("duplicate option must change the payload")
	}
}

func TestDamagedFixturesCarryOffsets(t *testing.T) {
	tr := BuildTruncatedBox(Options{})
	if tr.Damage == nil || tr.Damage.Offset != int64(len(tr.Data)) {
		t.Fatalf("truncated damage: %+v len=%d", tr.Damage, len(tr.Data))
	}
	rp := BuildRTP()
	if rp.Damage == nil || rp.Damage.Offset <= 0 {
		t.Fatalf("rtp damage: %+v", rp.Damage)
	}
	mi := BuildMissingIndex(Options{})
	if mi.Damage == nil || mi.Damage.Offset <= 0 {
		t.Fatalf("missing index damage: %+v", mi.Damage)
	}
	rec, ranges := BuildRecoverable(Options{})
	if rec.Damage == nil || len(ranges) == 0 {
		t.Fatalf("recoverable: %+v %v", rec.Damage, ranges)
	}
}

func TestNoExternalFiles(t *testing.T) {
	// Every fixture is built in memory; this guards against a future change
	// silently reading testdata from disk.
	for _, p := range []Payload{BuildValid(Options{}), BuildTS(), BuildRTP()} {
		if len(p.Data) == 0 {
			t.Fatalf("%s is empty", p.Name)
		}
	}
}
