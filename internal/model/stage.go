package model

import "fmt"

// Stage is a pipeline state. Allowed order:
//
//	created -> parsed -> composed -> exported -> verified
type Stage string

const (
	StageCreated  Stage = "created"
	StageParsed   Stage = "parsed"
	StageComposed Stage = "composed"
	StageExported Stage = "exported"
	StageVerified Stage = "verified"
)

// stageOrder indexes legal forward transitions.
var stageOrder = []Stage{
	StageCreated,
	StageParsed,
	StageComposed,
	StageExported,
	StageVerified,
}

// stageIndex returns the position of a stage in the legal order.
func stageIndex(s Stage) (int, bool) {
	for i, v := range stageOrder {
		if v == s {
			return i, true
		}
	}
	return -1, false
}

// CanTransition reports whether moving from -> to is a legal single forward
// step or an idempotent re-entry of the current stage.
func CanTransition(from, to Stage) bool {
	a, okA := stageIndex(from)
	b, okB := stageIndex(to)
	if !okA || !okB {
		return false
	}
	return b == a || b == a+1
}

// Transition validates and returns the target stage. Re-entering the same
// stage is allowed so reruns can reconfirm an already reached state.
func Transition(from, to Stage) (Stage, error) {
	if !CanTransition(from, to) {
		return from, NewFieldError(KindInvalidState, "", -1, "stage",
			fmt.Sprintf("illegal stage transition %s -> %s", from, to))
	}
	return to, nil
}

// IsFinal reports whether the pipeline has reached the verified stage.
func IsFinal(s Stage) bool { return s == StageVerified }
