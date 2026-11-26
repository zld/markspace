package recover

import (
	"os"

	merr "mediapack/internal/errors"
	"mediapack/internal/fanout"
	"mediapack/internal/manifest"
	"mediapack/internal/ts"
)

// Report is the verification metrics rendered by the verify command.
type Report struct {
	RoundtripOK      int
	RoundtripTotal   int
	RoundtripRate    float64
	KeyHits          int
	KeyTotal         int
	KeyframeRate     float64
	HashOK           int
	HashTotal        int
	FragmentChecksOK int
	FragmentTotal    int
	BoundaryPackets  int
	Aligned          bool
	Counts           map[merr.Category]int
	Pass             bool
}

// Verify loads the ledger and independently re-parses and re-hashes everything.
func Verify(workDir string) (*Report, *manifest.Manifest, error) {
	work, err := checkWork(workDir)
	if err != nil {
		return nil, nil, err
	}
	m, err := manifest.Load(work)
	if err != nil {
		return nil, nil, err
	}
	rep, err := evaluate(work, m)
	if err != nil {
		return rep, m, err
	}
	_ = m.Save(work)
	return rep, m, nil
}

func evaluate(work string, m *manifest.Manifest) (*Report, error) {
	rep := &Report{Counts: map[merr.Category]int{}}
	for _, is := range m.Issues {
		rep.Counts[is.Category]++
	}

	srcFull, err := manifest.Resolve(work, m.SourceRelPath)
	if err != nil {
		return rep, err
	}
	src, err := os.Open(srcFull)
	if err != nil {
		return rep, err
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return rep, err
	}
	analysis, err := fanout.Analyze(src, st.Size())
	if err != nil {
		return rep, err
	}

	// Roundtrip: independently re-digest each interval and compare to ledger.
	for _, ir := range m.Intervals {
		rep.RoundtripTotal++
		length := ir.EndOffset - ir.StartOffset
		if _, err := src.Seek(ir.StartOffset, 0); err != nil {
			continue
		}
		d, _, err := ts.DigestRange(src, ir.StartOffset, length, nil)
		if err == nil && d == ir.StructuralDigest {
			rep.RoundtripOK++
		}
	}
	if rep.RoundtripTotal > 0 {
		rep.RoundtripRate = float64(rep.RoundtripOK) / float64(rep.RoundtripTotal)
	} else {
		rep.RoundtripRate = 1
	}

	// Keyframe hit rate against ledger keyframe offsets on the fresh index.
	for _, ir := range m.Intervals {
		rep.KeyTotal++
		if analysis.KeyIndex.Exact(ir.KeyframeOffset) != nil {
			rep.KeyHits++
		}
	}
	if rep.KeyTotal > 0 {
		rep.KeyframeRate = float64(rep.KeyHits) / float64(rep.KeyTotal)
	} else {
		rep.KeyframeRate = 1
	}

	// Per-fragment hashes + structural MP4/RTP checks + fan-in set.
	var parsed []*fanout.ParsedFragment
	for i := range m.Artifacts {
		art := &m.Artifacts[i]
		rep.HashTotal++
		if hashOK(work, art) {
			rep.HashOK++
		}
		rep.FragmentTotal++
		full, err := manifest.Resolve(work, art.RelPath)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		if _, err := fanout.Check(b); err == nil {
			rep.FragmentChecksOK++
		}
		if pf, err := fanout.ParseFragment(b); err == nil {
			parsed = append(parsed, pf)
		}
	}

	fi := fanout.FanIn(parsed)
	rep.Aligned = fi.Aligned
	rep.BoundaryPackets = fi.MaxErrorPackets

	rep.Pass = rep.RoundtripOK == rep.RoundtripTotal &&
		rep.KeyHits == rep.KeyTotal &&
		rep.HashOK == rep.HashTotal &&
		rep.FragmentChecksOK == rep.FragmentTotal &&
		rep.BoundaryPackets <= 1 && rep.Aligned &&
		countInvalid(work, m) == 0

	m.RoundtripOK, m.RoundtripTotal = rep.RoundtripOK, rep.RoundtripTotal
	m.KeyframeHits, m.KeyframeTotal = rep.KeyHits, rep.KeyTotal
	m.BoundaryErrorPackets = rep.BoundaryPackets
	m.FanInAligned = rep.Aligned
	return rep, nil
}
