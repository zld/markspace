package pipeline

import (
	"strconv"

	"mediatimeline/asset"
	"mediatimeline/journal"
)

// commitJournal durably commits each merged interval. Reruns skip already
// committed keys, so no interval is committed twice. In corrupt mode an
// interrupted write is simulated once and recovered on the next Open.
func commitJournal(cfg Config, merged asset.MergeResult, res *Result, fail func(Failure)) {
	j, rb, err := journal.Open(cfg.OutDir)
	if err != nil {
		fail(Failure{Code: "JOURNAL_OPEN_FAILED", Stage: StageJournal, Message: err.Error()})
		return
	}
	defer j.Close()

	if rb != nil && rb.DiscardedWAL > 0 {
		res.RollbackFromNS = rb.FromWatermark
	}

	simulated := false
	for _, mi := range merged.Intervals {
		v := mi.Selected
		key := v.ProgramKey + "/" + v.TrackID + "/" +
			strconv.FormatInt(v.Interval.StartNS, 10) + "-" + strconv.FormatInt(v.Interval.EndNS, 10)
		rec := journal.Record{
			Key: key, ProgramKey: v.ProgramKey, TrackID: v.TrackID,
			StartNS: v.Interval.StartNS, EndNS: v.Interval.EndNS,
			Source: v.Source, Digest: v.Digest,
		}
		// In truncated mode, crash exactly once while committing the interval
		// adjacent to the damaged segment to exercise recovery end to end.
		if cfg.Corrupt == "truncated" && !simulated && v.Interval.StartNS >= 3_000_000_000 {
			simulated = true
			if err := j.SimulateInterruptedWrite(rec); err != nil {
				fail(Failure{Code: "JOURNAL_SIMULATE_FAILED", Stage: StageJournal, Message: err.Error()})
			}
			// Reopen immediately: recovery discards the partial commit.
			j.Close()
			var rb2 *journal.Rollback
			j, rb2, err = journal.Open(cfg.OutDir)
			if err != nil {
				fail(Failure{Code: "JOURNAL_RECOVER_FAILED", Stage: StageJournal, Message: err.Error()})
				return
			}
			if rb2 != nil && rb2.DiscardedWAL > 0 {
				res.RollbackFromNS = rb2.FromWatermark
			}
		}
		committed, cerr := j.Commit(rec)
		if cerr != nil {
			fail(Failure{Code: "JOURNAL_COMMIT_FAILED", Stage: StageJournal,
				Asset: v.Source, Message: cerr.Error()})
			continue
		}
		_ = committed
	}
}
