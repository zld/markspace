package pipeline

import (
	"mediaroute/internal/journal"
)

// crashHook maps the CLI crash-at name to a concrete journal point.
func crashHook(name string) journal.CrashHook {
	return func(point string) error {
		switch name {
		case journal.PointIndexCommit:
			if point == journal.PointAfterWALCommit+":"+indexStage(10) {
				return errSimulatedCrash
			}
		case "segment-commit":
			if point == journal.PointAfterWALCommit+":seg-0000" {
				return errSimulatedCrash
			}
		case "wal-begin":
			if point == journal.PointAfterWALBegin+":seg-0000" {
				return errSimulatedCrash
			}
		}
		return nil
	}
}

var errSimulatedCrash = clockError("simulated process crash")

type clockError string

func (e clockError) Error() string { return string(e) }

func segmentName(i int) string { return "seg-" + pad4(i) }

func indexStage(ssrc uint32) string { return "index-" + utoa(uint64(ssrc)) }

func pad4(i int) string {
	s := utoa(uint64(i))
	for len(s) < 4 {
		s = "0" + s
	}
	return s
}
