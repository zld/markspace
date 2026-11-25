package selftest

import (
	"fmt"
	"path/filepath"

	"media-audit/internal/manifest"
	"media-audit/internal/pipeline"
	"media-audit/internal/recover"
	"media-audit/internal/samplegen"
)

type Result struct {
	Manifest *manifest.Manifest
	Failures []string
}

func mappingFor(s *samplegen.Sample) manifest.SampleMapping {
	return manifest.SampleMapping{
		SampleID:             s.ID,
		Kind:                 s.Kind,
		StableKey:            s.StableKey,
		PrimaryPath:          firstOrEmpty(s.Paths),
		ExpectedDamageOffset: s.ExpectedDamageOffset,
	}
}

func firstOrEmpty(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[0]
}

// Run generates fixtures, executes the pipeline twice, verifies acceptance
// invariants, and builds the stable manifest.
func Run(fixtureDir, stateDir string) (*Result, error) {
	bundle, err := samplegen.Generate(fixtureDir)
	if err != nil {
		return nil, fmt.Errorf("generate fixtures: %w", err)
	}
	p, err := pipeline.New(stateDir)
	if err != nil {
		return nil, fmt.Errorf("open pipeline state: %w", err)
	}
	defer p.Close()

	m1, err := execute(p, bundle)
	if err != nil {
		return nil, err
	}
	failures := Verify(bundle, m1)

	// A second in-process pass exercises verified done checkpoints. The CLI
	// acceptance check starts a new process and exercises the same files.
	m2, err := execute(p, bundle)
	if err != nil {
		return nil, err
	}
	if m1.Digest != m2.Digest {
		failures = append(failures, fmt.Sprintf("in-process repeat digest mismatch: %s != %s", m1.Digest, m2.Digest))
	}
	if len(m1.Items) != len(m2.Items) {
		failures = append(failures, fmt.Sprintf("repeat item count mismatch: %d != %d", len(m1.Items), len(m2.Items)))
	}
	return &Result{Manifest: m1, Failures: failures}, nil
}

func execute(p *pipeline.Pipeline, bundle *samplegen.Bundle) (*manifest.Manifest, error) {
	merger := manifest.NewMerger()
	items := make([]*manifest.Item, 0, len(bundle.Samples))
	mappings := make([]manifest.SampleMapping, 0, len(bundle.Samples))
	for _, s := range bundle.Samples {
		item, err := p.ProcessSample(s)
		if err != nil {
			return nil, fmt.Errorf("process %s: %w", s.ID, err)
		}
		merger.Submit(item)
		mappings = append(mappings, mappingFor(s))
	}
	items = merger.Items()
	return manifest.BuildManifest(items, mappings), nil
}

func Verify(bundle *samplegen.Bundle, m *manifest.Manifest) []string {
	var failures []string
	byKey := make(map[string]*manifest.Item, len(m.Items))
	for _, it := range m.Items {
		byKey[it.StableKey] = it
	}

	for _, s := range bundle.Samples {
		it := byKey[s.StableKey]
		if it == nil {
			failures = append(failures, s.ID+": missing manifest item")
			continue
		}
		if s.Normal && it.ErrorCount != 0 {
			failures = append(failures, fmt.Sprintf("%s: normal sample has %d errors: %v", s.ID, it.ErrorCount, codes(it)))
		}
		if s.ExpectedCode != "" {
			var offset int64 = -1
			found := false
			for _, e := range it.Errors {
				if e.Code == s.ExpectedCode {
					found = true
					offset = e.Offset
					break
				}
			}
			if !found {
				failures = append(failures, fmt.Sprintf("%s: expected code %s not found in %v", s.ID, s.ExpectedCode, codes(it)))
			} else if offset != s.ExpectedDamageOffset {
				failures = append(failures, fmt.Sprintf("%s: failure offset %d != generated damage byte %d", s.ID, offset, s.ExpectedDamageOffset))
			}
			byteOK := false
			for _, d := range it.Deviations {
				if d.Kind == "damage-offset" {
					byteOK = d.WithinTol
				}
			}
			if !byteOK {
				failures = append(failures, s.ID+": byte deviation is not zero")
			}
		}
		for _, d := range it.Deviations {
			if d.Unit == "unified_tick" && !d.WithinTol {
				failures = append(failures, fmt.Sprintf("%s: time conversion error %.3f ticks exceeds 1", s.ID, d.Delta))
			}
		}
		if it.Recovery != nil {
			assertDisjointMerged(s.ID, "successful", it.Recovery.Successful, &failures)
			assertDisjointMerged(s.ID, "recomputed", it.Recovery.Recomputed, &failures)
			for _, ok := range it.Recovery.Successful {
				for _, rm := range it.Recovery.Recomputed {
					if ok.Start < rm.End && rm.Start < ok.End {
						failures = append(failures, fmt.Sprintf("%s: successful range %+v overlaps recomputed %+v", s.ID, ok, rm))
					}
				}
			}
		}
	}
	return failures
}

func assertDisjointMerged(id, name string, rs []recover.Range, failures *[]string) {
	merged := recover.MergeRanges(rs)
	if len(merged) != len(rs) {
		*failures = append(*failures, fmt.Sprintf("%s: %s ranges contain duplicates/overlaps: %+v", id, name, rs))
	}
}

func codes(it *manifest.Item) []string {
	out := make([]string, 0, len(it.Errors))
	for _, e := range it.Errors {
		out = append(out, string(e.Code))
	}
	return out
}

func DefaultFixtureDir() string {
	return filepath.Join("/tmp", "media-audit-fixtures")
}

func DefaultStateDir() string {
	return filepath.Join("/tmp", "media-audit-state")
}
