package normalize

import (
	"testing"

	"mediacheck/internal/dash"
	"mediacheck/internal/fixture"
	"mediacheck/internal/model"
	"mediacheck/internal/safeio"
)

func parse(t *testing.T, faults []model.FaultKind) *dash.Manifest {
	t.Helper()
	prog := fixture.Generate(17, faults)
	if err := fixture.Materialize(prog); err != nil {
		t.Fatal(err)
	}
	m, err := dash.ParseMPD(prog, safeio.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestValidateHealthy(t *testing.T) {
	rep := Validate(parse(t, nil))
	if len(rep.Conflicts) != 0 {
		t.Fatalf("healthy program has conflicts: %+v", rep.Conflicts)
	}
}

func TestValidateThreeConflictTypes(t *testing.T) {
	rep := Validate(parse(t, []model.FaultKind{model.FaultMetaConflict}))
	fields := map[Field]bool{}
	for _, c := range rep.Conflicts {
		fields[c.Field] = true
		if c.Range.End <= c.Range.Start {
			t.Fatalf("conflict %s has invalid interval %+v", c.Field, c.Range)
		}
		if c.Representation == "" {
			t.Fatal("conflict not bound to a representation")
		}
	}
	for _, want := range []Field{FieldChannels, FieldSampleRate, FieldColor} {
		if !fields[want] {
			t.Fatalf("missing conflict type %s, got %v", want, fields)
		}
	}
}

func TestUnknownFieldNotSilent(t *testing.T) {
	manifest := parse(t, nil)
	// Blank the color primaries on one video representation.
	for _, r := range manifest.Representations {
		if r.Kind == "video" && r.ID == "v-low" {
			r.Init.ColorPrimaries = 0
		}
	}
	rep := Validate(manifest)
	var sawUnknown bool
	for _, c := range rep.Conflicts {
		if c.Field == FieldColor && c.Unknown {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Fatal("unknown color field must be reported, not treated as a match")
	}
}

func TestUnknownAudioField(t *testing.T) {
	manifest := parse(t, nil)
	for _, r := range manifest.Representations {
		if r.Kind == "audio" && r.ID == "a-surround" {
			r.Init.SampleRate = 0
		}
	}
	rep := Validate(manifest)
	var sawUnknown bool
	for _, c := range rep.Conflicts {
		if c.Field == FieldSampleRate && c.Unknown {
			sawUnknown = true
		}
	}
	if !sawUnknown {
		t.Fatal("unknown sample rate must be flagged")
	}
}
