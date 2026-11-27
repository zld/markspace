// Package migrate performs a staged, resumable manifest migration. Each stage
// leaves a durable rollback point; an interruption resumes from the last
// committed stage without re-appending entries.
package migrate

// Stage names appear verbatim in the rollback point and in terminal output.
const (
	StagePrepare = "prepare"
	StageWrite   = "write-temp"
	StageVerify  = "verify"
	StageReplace = "atomic-replace"
	StageDone    = "done"
)

const (
	pointName    = "migration.point.json"
	tempName     = "manifest.v2.json.tmp"
	backupSuffix = ".rollback"
)

// StageOrder is the fixed pipeline order, used by tests and reporting.
func StageOrder() []string {
	return []string{StagePrepare, StageWrite, StageVerify, StageReplace, StageDone}
}

// IsKnownStage reports whether s names a pipeline stage.
func IsKnownStage(s string) bool {
	for _, st := range StageOrder() {
		if st == s {
			return true
		}
	}
	return false
}
