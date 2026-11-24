package pipeline

import (
	"encoding/json"
	"os"

	"mediascan/internal/fault"
	"mediascan/internal/index"
	"mediascan/internal/report"
)

// Summary is the quantitative result of one full pipeline run.
type Summary struct {
	TotalSegments        int        `json:"total_segments"`
	DamagedSegments      int        `json:"damaged_segments"`
	RoundtripConsistency float64    `json:"roundtrip_consistency"`
	KeyframeHitRate      float64    `json:"keyframe_hit_rate"`
	ProcessableAfter     int        `json:"processable_after_damage"`
	VerifiedPrefixBytes  int64      `json:"verified_prefix_bytes"`
	DamageOffsets        []int64    `json:"damage_offsets"`
	IsolatedRanges       [][2]int64 `json:"isolated_ranges"`
	Fingerprint          string     `json:"fingerprint"`
	ExitCode             int        `json:"exit_code"`
}

// Checkpoint is the on-disk resume record. Re-running from a checkpoint
// must reproduce identical hashes, index and fault offsets.
type Checkpoint struct {
	Fingerprint string                `json:"fingerprint"`
	Results     []CheckpointedSegment `json:"results"`
	Entries     []index.Entry         `json:"entries"`
}

// CheckpointedSegment stores the per-segment hashes and verdict needed to
// verify a resumed run without retaining raw media bytes.
type CheckpointedSegment struct {
	Segment     string `json:"segment"`
	MediaSeq    uint64 `json:"media_seq"`
	InputHash   string `json:"input_sha256"`
	OutputHash  string `json:"output_sha256"`
	Damaged     bool   `json:"damaged"`
	RoundtripOK bool   `json:"roundtrip_ok"`
}

// Run executes all three stages over the ordered inputs. A damaged
// segment never aborts the run: its prefix stays verified, its index
// entries are quarantined, and independent later segments continue.
func (p *Pipeline) Run(inputs []SegmentInput) *Summary {
	st := &TransformState{}
	var queries []index.Query
	damaged := 0
	okCount := 0
	var processableAfter int
	sawDamage := false
	var offsets, prefixes []int64

	for _, in := range inputs {
		ps := p.ParseStage(in)
		ns, next := p.TransformStage(ps, st)
		st = next
		outHash, err := p.StoreHash(ps, ns)
		if err != nil {
			p.addValidateFault(in, fault.KindRoundtripMismatch, err.Error())
		}
		roundtripOK := p.ValidateStage(ps, ns, outHash)

		if ps.Damaged {
			damaged++
			sawDamage = true
			if ps.MP4 != nil {
				offsets = append(offsets, ps.MP4.UnsafeBoundary)
				prefixes = append(prefixes, ps.MP4.SafePrefix)
			}
		} else {
			okCount++
			if sawDamage {
				processableAfter++
			}
			for _, s := range ns.Samples {
				if s.Keyframe {
					queries = append(queries, index.Query{
						Segment: in.Name, SampleIndex: s.Index,
					})
				}
			}
		}
		p.results = append(p.results, &Result{
			Parsed: ps, Normalized: ns, OutputHash: outHash,
			RoundtripOK: roundtripOK,
		})
	}

	total := len(inputs)
	consistency := 1.0
	if total > 0 {
		consistency = float64(okCount) / float64(total)
	}
	hitRate := p.Index.HitRate(queries)
	summary := &Summary{
		TotalSegments: total, DamagedSegments: damaged,
		RoundtripConsistency: roundN(consistency),
		KeyframeHitRate:      roundN(hitRate),
		ProcessableAfter:     processableAfter,
		DamageOffsets:        offsets,
		Fingerprint:          p.Hasher.Fingerprint(),
	}
	if len(prefixes) > 0 {
		summary.VerifiedPrefixBytes = max64(prefixes)
	}
	summary.IsolatedRanges = p.isolatedRanges()
	summary.ExitCode = p.ExitCode(summary)
	return summary
}

func (p *Pipeline) isolatedRanges() [][2]int64 {
	var out [][2]int64
	for _, r := range p.results {
		if r.Parsed == nil || !r.Parsed.Damaged || r.Parsed.MP4 == nil {
			continue
		}
		start := r.Parsed.MP4.UnsafeBoundary
		end := int64(len(r.Parsed.Input.Data))
		out = append(out, [2]int64{start, end})
	}
	return out
}

// ExitCode applies the contract: 0 clean, 2 media/manifest damage, and 1
// for internal/parameter errors (reported separately by the driver).
func (p *Pipeline) ExitCode(s *Summary) int {
	if p.Log.HasError() || s.DamagedSegments > 0 {
		return 2
	}
	return 0
}

// SaveCheckpoint writes the run fingerprint, per-segment verdicts and
// index entries for later verification.
func (p *Pipeline) SaveCheckpoint(path string) error {
	cp := Checkpoint{Fingerprint: p.Hasher.Fingerprint(), Entries: p.Index.Snapshot()}
	for _, r := range p.results {
		cp.Results = append(cp.Results, CheckpointedSegment{
			Segment: r.Parsed.Input.Name, MediaSeq: r.Parsed.Input.MediaSeq,
			InputHash: r.Parsed.InputHash, OutputHash: r.OutputHash,
			Damaged: r.Parsed.Damaged, RoundtripOK: r.RoundtripOK,
		})
	}
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

// LoadCheckpoint reads a previously saved checkpoint.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, err
	}
	return &cp, nil
}

// VerifyCheckpoint confirms a fresh run reproduces the stored fingerprint
// and every per-segment hash, which is the resume consistency guarantee.
func VerifyCheckpoint(cp *Checkpoint, p *Pipeline) error {
	if cp.Fingerprint != p.Hasher.Fingerprint() {
		return report.ErrResumeMismatch("fingerprint")
	}
	current := p.Results()
	if len(cp.Results) != len(current) {
		return report.ErrResumeMismatch("segment count")
	}
	for i, saved := range cp.Results {
		got := current[i]
		if saved.InputHash != got.Parsed.InputHash ||
			saved.OutputHash != got.OutputHash ||
			saved.Damaged != got.Parsed.Damaged {
			return report.ErrResumeMismatch(saved.Segment)
		}
	}
	return nil
}

func roundN(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

func max64(vs []int64) int64 {
	m := vs[0]
	for _, v := range vs[1:] {
		if v > m {
			m = v
		}
	}
	return m
}
