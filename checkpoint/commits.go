package checkpoint

import "sort"

// LastCommitted returns the highest committed block across audio/stream.
func (f *File) LastCommitted() int {
	best := -1
	for _, s := range []Stage{StageAudio, StageStream} {
		if st, ok := f.Stages[s]; ok && st.CommittedBlock > best {
			best = st.CommittedBlock
		}
	}
	return best
}

// sortedKeys orders stage keys for deterministic diagnostics.
func sortedKeys(m map[Stage]*StageState) []Stage {
	keys := make([]Stage, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}
