package fixture

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"hlsflow/internal/config"
	"hlsflow/internal/safeio"
)

// Scenario names accepted by the fixture command.
const (
	ScenarioClean  = "clean"
	ScenarioFaults = "faults"
)

// defaultRates maps the three demo rates to stable rendition names and a
// per-rendition PTS offset in ticks used to create measurable drift.
var defaultRates = []struct {
	name   string
	rate   int
	offset int64
}{
	{"480p", 800000, 0},
	{"720p", 1800000, 5400},   // +60ms vs the 480p timeline
	{"1080p", 3500000, 10800}, // +120ms
}

// ArrivalManifest records the order in which source files were dropped, plus
// which declared sequences are intentionally absent. Build reads it to count
// reordered arrivals; the gaps still come from the real on-disk state.
type ArrivalManifest struct {
	Order    []int `json:"order"`
	Missing  []int `json:"missing"`
	FirstSeq int   `json:"first_seq"`
	LastSeq  int   `json:"last_seq"`
}

// Result summarizes a generated fixture tree.
type Result struct {
	JobPath    string
	InputRoot  string
	Renditions []config.Rendition
}

// Generate writes a full fixture tree under dir for the given rates and
// scenario. Rates nil selects the default three-rendition ladder.
func Generate(dir, scenario string, rates []int) (*Result, error) {
	ladder, err := pickLadder(rates)
	if err != nil {
		return nil, err
	}
	root, err := safeio.EnsureRoot(dir)
	if err != nil {
		return nil, err
	}
	const first, last = 0, 10 // 11 source segments per rendition
	job := config.Job{
		GapTimeout:     config.Duration{},
		DriftTolerance: config.Duration{},
	}
	res := &Result{InputRoot: root.String()}
	for _, rung := range ladder {
		r := config.Rendition{
			Name: rung.name, Bitrate: rung.rate, Codec: "h264",
			FirstSeq: first, LastSeq: last,
		}
		job.Renditions = append(job.Renditions, r)
		res.Renditions = append(res.Renditions, r)
		if err := writeRendition(root, rung, first, last, scenario); err != nil {
			return nil, err
		}
	}
	if scenario == ScenarioFaults {
		applyFaultScenario(root, &job, ladder, first, last)
	}
	jobPath := root.MustJoin("job.json")
	job.GapTimeout = config.Duration{}
	job.DriftTolerance = config.Duration{}
	if err := writeJSON(root, "job.json", &job); err != nil {
		return nil, err
	}
	res.JobPath = jobPath
	return res, nil
}

func pickLadder(rates []int) ([]struct {
	name   string
	rate   int
	offset int64
}, error) {
	if len(rates) == 0 {
		return defaultRates, nil
	}
	seen := map[int]bool{}
	out := make([]struct {
		name   string
		rate   int
		offset int64
	}, 0, len(rates))
	for i, rate := range rates {
		if rate <= 0 || seen[rate] {
			return nil, fmt.Errorf("invalid or duplicate rate %d", rate)
		}
		seen[rate] = true
		name := rungName(rate, i)
		offset := int64(i) * 5400 // deterministic, increasing drift
		out = append(out, struct {
			name   string
			rate   int
			offset int64
		}{name, rate, offset})
	}
	return out, nil
}

// rungName gives a stable label for ad-hoc rates, or the well-known names for
// the demo ladder.
func rungName(rate, idx int) string {
	for _, d := range defaultRates {
		if d.rate == rate {
			return d.name
		}
	}
	return fmt.Sprintf("rendition-%d", idx)
}

func writeRendition(root *safeio.Root, rung struct {
	name   string
	rate   int
	offset int64
}, first, last int, scenario string) error {
	relDir := filepath.Join("inbox", rung.name)
	if err := root.MkdirAll(relDir); err != nil {
		return err
	}
	manifest := ArrivalManifest{FirstSeq: first, LastSeq: last}
	for seq := first; seq <= last; seq++ {
		manifest.Order = append(manifest.Order, seq)
	}
	if scenario == ScenarioFaults {
		// Two adjacent swaps -> two reordered arrivals per rendition.
		manifest.Order = swap(manifest.Order, 2, 3)
		manifest.Order = swap(manifest.Order, 7, 8)
		// One deliberately missing segment.
		manifest.Missing = []int{5}
	}
	sort.Ints(manifest.Missing)
	missing := map[int]bool{}
	for _, s := range manifest.Missing {
		missing[s] = true
	}
	for _, seq := range manifest.Order {
		if missing[seq] {
			continue
		}
		data := segmentTS(seq, rung.offset)
		file := filepath.Join(relDir, fmt.Sprintf("seg-%05d.ts", seq))
		if err := root.AtomicWrite(file, data); err != nil {
			return err
		}
	}
	return writeJSON(root, filepath.Join(relDir, "arrival.json"), &manifest)
}

func applyFaultScenario(root *safeio.Root, job *config.Job, ladder []struct {
	name   string
	rate   int
	offset int64
}, first, last int) {
	// Truncated last packet on one segment of the first rendition: the file
	// ends with a partial 188-byte packet, which the parser must flag.
	firstRung := ladder[0]
	truncRel := filepath.Join("inbox", firstRung.name, "seg-00003.ts")
	if data, err := root.ReadFile(truncRel); err == nil {
		trimmed := append([]byte(nil), data[:len(data)-37]...)
		_ = root.AtomicWrite(truncRel, trimmed)
	}
	// Checksum mismatch on one segment of the middle rendition. The fault is
	// applied at verify time so the build path stays byte-identical.
	mid := ladder[len(ladder)/2]
	job.Faults = append(job.Faults, config.FaultInjection{
		Rendition: mid.name, Segment: 9, Offset: 17, XOR: 0xA5,
	})
}

func writeJSON(root *safeio.Root, rel string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return root.AtomicWrite(rel, append(data, '\n'))
}

func swap(in []int, a, b int) []int {
	out := append([]int(nil), in...)
	out[a], out[b] = out[b], out[a]
	return out
}
