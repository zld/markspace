package fixture_test

import (
	"bytes"
	"testing"

	"mediaroute/internal/fixture"
)

// TestFixturesAreDeterministic proves identical seeds yield identical bytes,
// which is what makes the demo's digest requirement meaningful.
func TestFixturesAreDeterministic(t *testing.T) {
	for _, d := range []fixture.Damage{
		fixture.DamageNone, fixture.DamageSequenceGap, fixture.DamageTfdtRegression,
		fixture.DamageTruncatedBox, fixture.DamageCorruptIndex,
	} {
		a, err := fixture.Generate(fixture.DefaultParams(17), d)
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		b, err := fixture.Generate(fixture.DefaultParams(17), d)
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		if !bytes.Equal(a.Bytes, b.Bytes) {
			t.Fatalf("%s: identical seeds produced different bytes", d)
		}
	}
}

// TestFixturesDifferBySeed guards against a generator that ignores its seed.
func TestFixturesDifferBySeed(t *testing.T) {
	a, _ := fixture.Generate(fixture.DefaultParams(17), fixture.DamageNone)
	b, _ := fixture.Generate(fixture.DefaultParams(18), fixture.DamageNone)
	if bytes.Equal(a.Bytes, b.Bytes) {
		t.Fatal("different seeds produced identical bytes")
	}
}

func TestAllDamageVariantsGenerate(t *testing.T) {
	for _, d := range []fixture.Damage{
		fixture.DamageNone, fixture.DamageSequenceGap, fixture.DamageTfdtRegression,
		fixture.DamageTruncatedBox, fixture.DamageCorruptIndex,
		fixture.DamageMissingInit, fixture.DamageZeroTimescale,
	} {
		g, err := fixture.Generate(fixture.DefaultParams(17), d)
		if err != nil {
			t.Fatalf("%s: %v", d, err)
		}
		if len(g.Bytes) == 0 {
			t.Fatalf("%s: empty fixture", d)
		}
		if d == fixture.DamageTruncatedBox {
			if g.TruncatedBoxes != 1 {
				t.Fatalf("truncated fixture should record one truncated box, got %d", g.TruncatedBoxes)
			}
		}
		if d == fixture.DamageCorruptIndex && g.CorruptIndexOffset == 0 {
			t.Fatal("corrupt index fixture should record the injected offset")
		}
	}
}

func TestMissingInitOmitsMoov(t *testing.T) {
	g, err := fixture.Generate(fixture.DefaultParams(17), fixture.DamageMissingInit)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if bytes.Contains(g.Bytes, []byte("moov")) {
		t.Fatal("missing-init fixture must not contain a moov box")
	}
	if !bytes.Contains(g.Bytes, []byte("moof")) {
		t.Fatal("missing-init fixture should still carry fragments")
	}
}

func TestFixtureExpectedCounts(t *testing.T) {
	g, err := fixture.Generate(fixture.DefaultParams(17), fixture.DamageTruncatedBox)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	// Six fragments, five samples per track per fragment, minus the dropped one.
	want := (g.Params.Fragments - 1) * g.Params.SamplesPerFrag * 2
	if g.ExpectedValidSamples != want {
		t.Fatalf("want %d valid samples, got %d", want, g.ExpectedValidSamples)
	}
	if g.RecoverableFragmentCount() < 1 {
		t.Fatal("truncated fixture should leave later fragments recoverable")
	}
}

func TestGenerateRejectsInvalidParams(t *testing.T) {
	bad := fixture.DefaultParams(17)
	bad.Fragments = 0
	if _, err := fixture.Generate(bad, fixture.DamageNone); err == nil {
		t.Fatal("expected rejection of zero fragments")
	}
	bad = fixture.DefaultParams(17)
	bad.VideoTimescale = 0
	if _, err := fixture.Generate(bad, fixture.DamageNone); err == nil {
		t.Fatal("expected rejection of zero timescale")
	}
}

func TestTargetBitrateShapesBytes(t *testing.T) {
	low := fixture.DefaultParams(17)
	low.TargetBPS = 500_000
	high := fixture.DefaultParams(17)
	high.TargetBPS = 8_000_000
	gl, _ := fixture.Generate(low, fixture.DamageNone)
	gh, _ := fixture.Generate(high, fixture.DamageNone)
	if len(gh.Bytes) <= len(gl.Bytes) {
		t.Fatalf("higher target should yield larger fixture: %d vs %d", len(gh.Bytes), len(gl.Bytes))
	}
}
