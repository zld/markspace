package fixture

import "testing"

func TestGeneratePCMIsBoundedAndDeterministic(t *testing.T) {
	a := GeneratePCM(DefaultPlan(KindClean))
	b := GeneratePCM(DefaultPlan(KindClean))
	if a.Frames != b.Frames || a.StartTS != b.StartTS {
		t.Fatal("PCM generation is not deterministic")
	}
	if len(a.PCM) != len(b.PCM) {
		t.Fatal("PCM length varies")
	}
	for i := range a.PCM {
		if a.PCM[i] != b.PCM[i] {
			t.Fatalf("PCM differs at %d", i)
		}
		if a.PCM[i] < -1 || a.PCM[i] > 1 {
			t.Fatalf("sample %d out of nominal range: %f", i, a.PCM[i])
		}
	}
	wrap := GeneratePCM(DefaultPlan(KindWrap))
	if wrap.StartTS < 1<<31 {
		t.Fatalf("wrap start %d is not near the 32-bit limit", wrap.StartTS)
	}
}
