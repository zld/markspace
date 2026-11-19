package program

import (
	"fmt"
	"sort"
	"strings"
)

func describePAT(p *PATState) string {
	nums := make([]uint16, 0, len(p.Programs))
	for n := range p.Programs {
		nums = append(nums, n)
	}
	sort.Slice(nums, func(i, j int) bool { return nums[i] < nums[j] })
	out := fmt.Sprintf("tsid=%d", p.TransportStreamID)
	for _, n := range nums {
		out += fmt.Sprintf(" %d->%d", n, p.Programs[n])
	}
	return out
}

func describePATDiff(a, b *PATState) string {
	var added, removed, changed []uint16
	for p := range b.Programs {
		if _, ok := a.Programs[p]; !ok {
			added = append(added, p)
		}
	}
	for p, pid := range a.Programs {
		np, ok := b.Programs[p]
		if !ok {
			removed = append(removed, p)
		} else if np != pid {
			changed = append(changed, p)
		}
	}
	parts := make([]string, 0, 3)
	for _, item := range []struct {
		label string
		ids   []uint16
	}{{"added", added}, {"removed", removed}, {"pmt_pid_changed", changed}} {
		if len(item.ids) == 0 {
			continue
		}
		sort.Slice(item.ids, func(i, j int) bool { return item.ids[i] < item.ids[j] })
		parts = append(parts, fmt.Sprintf("%s %v", item.label, item.ids))
	}
	if len(parts) == 0 {
		return "no program changes"
	}
	return strings.Join(parts, ", ")
}

// wrappedVersion reports whether moving from prev to next plausibly crossed
// the 5-bit version wrap point. A jump of a few values is a normal increment
// even if numerically smaller; a large backward jump is a wrap.
func wrappedVersion(prev, next uint8) bool {
	return next < prev && int(prev)-int(next) > 16
}
