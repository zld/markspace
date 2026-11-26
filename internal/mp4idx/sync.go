package mp4idx

import (
	"github.com/abema/go-mp4"

	"mediamerge/internal/fault"
)

// resolveSync determines the sync-sample (keyframe) set.
//
// Rules:
//   - A valid stss whose sample numbers all fall inside the table is trusted.
//   - A malformed stss is rebuilt only when the box flags are sufficient to
//     validate the result (self-consistent ordering/range). Otherwise the
//     index is marked unrecoverable rather than guessed.
//   - With no stss at all, every sample is provably a sync sample per ISO.
func resolveSync(raw *rawTables, count uint32) (map[uint32]bool, bool, bool, []string) {
	set := map[uint32]bool{}
	if raw.stss == nil {
		for n := uint32(1); n <= count; n++ {
			set[n] = true
		}
		return set, false, false, nil
	}
	nums := raw.stss.SampleNumber
	if len(nums) != int(raw.stss.EntryCount) {
		return nil, false, true, []string{
			"stss entry count disagrees with its payload; sync state cannot be proven"}
	}
	var prev uint32
	for _, n := range nums {
		if n < 1 || n > count || n <= prev {
			// Flags present but not self-consistent: no safe reconstruction.
			return nil, false, true, []string{
				"stss sample numbers out of range or unsorted; index unrecoverable"}
		}
		prev = n
		set[n] = true
	}
	return set, false, false, nil
}

// ValidateSyncExplicit re-checks an externally supplied stss against the
// rebuilt sample count, returning a typed fault on any inconsistency.
func ValidateSyncExplicit(stss *mp4.Stss, count uint32) error {
	if stss == nil {
		return nil
	}
	if len(stss.SampleNumber) != int(stss.EntryCount) {
		return fault.New(fault.StageIndex, fault.KindUnrecoverable, "",
			"stss entry count mismatch")
	}
	var prev uint32
	for _, n := range stss.SampleNumber {
		if n < 1 || n > count || n <= prev {
			return fault.New(fault.StageIndex, fault.KindUnrecoverable, "",
				"stss sample number out of range or unsorted")
		}
		prev = n
	}
	return nil
}
