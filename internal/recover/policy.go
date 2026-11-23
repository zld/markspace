package recover

import "sort"

// Recoverable is a unit of work the policy may schedule once its track
// dependencies are ready.
type Recoverable struct {
	TrackNumber uint32
	TimeNs      int64
	Offset      int64
	// DependsOn lists track numbers that must be scheduled first.
	DependsOn []uint32
	// Priority breaks ties; lower values run earlier.
	Priority int
}

// Plan is an ordered recovery schedule.
type Plan struct {
	Order  []Recoverable
	Skipped []Recoverable // items whose dependencies never became ready
}

// BuildPolicyOrder schedules recoverables so that every dependency precedes
// its consumer. Among items whose dependencies are already satisfied, earlier
// source time wins, then priority, then offset for full determinism. Items
// whose dependencies are absent are reported rather than silently dropped.
func BuildPolicyOrder(items []Recoverable) *Plan {
	plan := &Plan{}
	known := make(map[uint32]bool, len(items))
	for _, it := range items {
		known[it.TrackNumber] = true
	}

	remaining := append([]Recoverable(nil), items...)
	scheduled := map[uint32]bool{}
	for len(remaining) > 0 {
		// Partition into ready and blocked on each pass.
		var ready, blocked []Recoverable
		for _, it := range remaining {
			if depsReady(it, scheduled, known) {
				ready = append(ready, it)
			} else {
				blocked = append(blocked, it)
			}
		}
		if len(ready) == 0 {
			// Nothing can unblock: the rest have unsatisfied dependencies.
			plan.Skipped = append(plan.Skipped, blocked...)
			break
		}
		sort.SliceStable(ready, func(i, j int) bool {
			a, b := ready[i], ready[j]
			if a.TimeNs != b.TimeNs {
				return a.TimeNs < b.TimeNs
			}
			if a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			return a.Offset < b.Offset
		})
		for _, it := range ready {
			plan.Order = append(plan.Order, it)
			scheduled[it.TrackNumber] = true
		}
		remaining = blocked
	}
	return plan
}

// depsReady reports whether every dependency is scheduled and known.
func depsReady(it Recoverable, scheduled, known map[uint32]bool) bool {
	for _, dep := range it.DependsOn {
		if !known[dep] || !scheduled[dep] {
			return false
		}
	}
	return true
}
