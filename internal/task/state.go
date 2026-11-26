// Package task persists the stage pipeline (new -> ingest -> decode ->
// normalize -> seal). Every stage writes its result and content hash
// atomically; on restart the checkpoint is validated before reuse, and
// any change to the input hash or decoder version invalidates every
// downstream stage.
package task

// Stage is one pipeline phase. Stages advance strictly in order.
type Stage string

const (
	StageNew       Stage = "new"
	StageIngest    Stage = "ingest"
	StageDecode    Stage = "decode"
	StageNormalize Stage = "normalize"
	StageSeal      Stage = "seal"
)

// stageOrder gives the strict advancement order.
var stageOrder = []Stage{StageNew, StageIngest, StageDecode, StageNormalize, StageSeal}

// CanAdvance reports whether next is a legal successor of s.
func CanAdvance(s, next Stage) bool {
	for i, st := range stageOrder {
		if st == s && i+1 < len(stageOrder) && stageOrder[i+1] == next {
			return true
		}
	}
	return false
}

// Index returns the numeric position of a stage (new=0).
func Index(s Stage) int {
	for i, st := range stageOrder {
		if st == s {
			return i
		}
	}
	return -1
}

// Status classifies the final output disposition of a task.
type Status string

const (
	// StatusPASS: the full target interval was produced within budget.
	StatusPASS Status = "PASS"
	// StatusPARTIAL: known gaps exist but every produced sample is valid.
	StatusPARTIAL Status = "PARTIAL"
	// StatusHOLD: an unlocatable interval prevents complete output.
	StatusHOLD Status = "HOLD"
)

// Spec is the target output specification embedded in the stable key.
type Spec struct {
	SampleRate uint32  `json:"sample_rate"`
	Channels   uint16  `json:"channels"`
	TargetRMS  float64 `json:"target_rms_db"`
	PeakCeilDB float64 `json:"peak_ceiling_db"`
}

// StageResult is one persisted stage artifact: its JSON payload file,
// content hash and the pipeline version that produced it.
type StageResult struct {
	Stage          Stage  `json:"stage"`
	PayloadFile    string `json:"payload_file"`
	PayloadSHA256  string `json:"payload_sha256"`
	DecoderVersion string `json:"decoder_version"`
}
