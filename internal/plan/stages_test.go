package plan

import "testing"

func TestLegalAndIllegalTransitions(t *testing.T) {
	legal := [][2]Stage{
		{StagePendingScan, StageScanned},
		{StagePendingScan, StageFailed},
		{StageScanned, StagePlanned},
		{StagePlanned, StageProcessing},
		{StageProcessing, StageVerified},
		{StageVerified, StageCompleted},
		{StageProcessing, StageFailed},
		{StageFailed, StagePendingScan},
	}
	for _, x := range legal {
		if err := Transition(x[0], x[1]); err != nil {
			t.Fatalf("%s -> %s should be legal: %v", x[0], x[1], err)
		}
	}

	illegal := [][2]Stage{
		{StagePendingScan, StageCompleted},
		{StageScanned, StageProcessing},
		{StageCompleted, StageProcessing},
		{StageCompleted, StageFailed},
		{StageFailed, StageProcessing},
		{StageProcessing, StageProcessing},
	}
	for _, x := range illegal {
		if err := Transition(x[0], x[1]); err == nil {
			t.Fatalf("%s -> %s should be illegal", x[0], x[1])
		}
	}
}
