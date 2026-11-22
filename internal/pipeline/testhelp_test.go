package pipeline

import (
	"encoding/json"
	"os"

	"mediad/internal/recovery"
)

// loadStore opens the on-disk journal for test manipulation.
func loadStore(path string) (*recovery.Checkpoint, error) {
	return recovery.LoadFile(path)
}

// truncateAfter keeps only stages up to and including keep, simulating a
// process that crashed before committing the rest.
func truncateAfter(cp *recovery.Checkpoint, path, keep string) error {
	var kept []recovery.StageRecord
	for _, st := range cp.Stages {
		kept = append(kept, st)
		if st.Name == keep {
			break
		}
	}
	cp.Stages = kept
	cp.LastComplete = keep
	cp.LastRecordID = kept[len(kept)-1].RecordsThrough
	data, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
