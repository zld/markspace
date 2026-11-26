package fixture

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"streamcheck/core"
)

// Faults selects which fault classes are injected into the fixtures.
type Faults struct {
	TruncatedBox   bool `json:"truncatedBox"`
	TimingJump     bool `json:"timingJump"`
	RTP            bool `json:"rtp"`
	MissingKeyframe bool `json:"missingKeyframe"`
	CorruptNAL     bool `json:"corruptNal"`
	MigrateFail    bool `json:"migrateFail"`
}

// ParseFaultSet parses "all", "none" or a comma-separated fault list.
func ParseFaultSet(s string) (Faults, error) {
	switch s {
	case "all":
		return Faults{true, true, true, true, true, true}, nil
	case "none":
		return Faults{}, nil
	}
	var f Faults
	for _, name := range strings.Split(s, ",") {
		switch strings.TrimSpace(name) {
		case "truncated-box":
			f.TruncatedBox = true
		case "timing-jump":
			f.TimingJump = true
		case "rtp":
			f.RTP = true
		case "missing-keyframe":
			f.MissingKeyframe = true
		case "corrupt-nal":
			f.CorruptNAL = true
		case "migrate-fail":
			f.MigrateFail = true
		default:
			return Faults{}, core.E(core.KindUsage, "fault-set", "unknown fault "+name, nil)
		}
	}
	return f, nil
}

// Plan is the ground truth of a generated fixture set: what the pipeline
// must report for this run to be correct.
type Plan struct {
	Seed            int64   `json:"seed"`
	FaultSet        string  `json:"faultSet"`
	Inputs          []string `json:"inputs"`
	CorruptSegment  int     `json:"corruptSegment"`
	CorruptOffset   int64   `json:"corruptOffset"`
	NormalGOPMaxMs  int64   `json:"normalGopMaxMs"`
	InjectedGOPMaxMs int64  `json:"injectedGopMaxMs"`
	Duplicates      int     `json:"duplicates"`
	Reordered       int     `json:"reordered"`
	BurstLost       int     `json:"burstLost"`
	Discontinuities int     `json:"discontinuities"`
	TimingJumps     int     `json:"timingJumps"`
}

// PlanFile is the plan file name inside the work directory.
const PlanFile = "plan.json"

// File names of the generated fixtures.
const (
	MPDFile       = "manifest.mpd"
	HLSFile       = "live.m3u8"
	FMP4Low       = "v800.mp4"
	FMP4High      = "v1600.mp4"
	TruncatedFile = "truncated.mp4"
	CaptureFile   = "capture.rtp"
	LegacyFile    = "legacy.m3u8"
	RecoveredFile = "recovered.bin"
)

// Generate writes the full fixture set into work and returns the plan.
func Generate(work string, seed int64, faultSet string) (*Plan, error) {
	faults, err := ParseFaultSet(faultSet)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, core.E(core.KindInput, "generate", "cannot create work dir", err).At(work, -1)
	}
	rnd := rand.New(rand.NewSource(seed))
	plan := &Plan{
		Seed: seed, FaultSet: faultSet,
		CorruptSegment: -1, CorruptOffset: -1,
		NormalGOPMaxMs: 2000, InjectedGOPMaxMs: 2000,
		Discontinuities: 1,
	}
	write := func(name string, data []byte) error {
		if err := os.WriteFile(filepath.Join(work, name), data, 0o644); err != nil {
			return core.E(core.KindInput, "generate", "cannot write fixture", err).At(name, -1)
		}
		plan.Inputs = append(plan.Inputs, name)
		return nil
	}

	if err := write(MPDFile, []byte(BuildMPD(faults.TimingJump))); err != nil {
		return nil, err
	}
	if faults.TimingJump {
		plan.TimingJumps = 2 // one per representation
	}
	if err := write(HLSFile, []byte(BuildHLS())); err != nil {
		return nil, err
	}

	segs := make([]SegmentSpec, 5)
	for i := range segs {
		segs[i] = SegmentSpec{DecodeTimeMs: uint64(i) * 2000, IDR: true}
	}
	if faults.MissingKeyframe {
		segs[2].IDR = false
		plan.InjectedGOPMaxMs = 4000
	}
	low := append([]SegmentSpec(nil), segs...)
	if faults.CorruptNAL {
		low[4].Corrupt = true
		plan.CorruptSegment = 4
	}
	data, corruptAt := BuildFMP4(rnd, "iso6", low)
	if faults.CorruptNAL {
		plan.CorruptOffset = corruptAt
	}
	if err := write(FMP4Low, data); err != nil {
		return nil, err
	}
	high, _ := BuildFMP4(rnd, "iso6", segs)
	if err := write(FMP4High, high); err != nil {
		return nil, err
	}

	if faults.TruncatedBox {
		if err := write(TruncatedFile, BuildTruncatedMP4()); err != nil {
			return nil, err
		}
	}

	var rtpFaults *RTPFaults
	if faults.RTP {
		f := DefaultRTPFaults()
		rtpFaults = &f
		plan.Duplicates, plan.Reordered, plan.BurstLost = 1, 2, 3
	}
	capture, err := BuildCapture(rnd, rtpFaults)
	if err != nil {
		return nil, core.E(core.KindInput, "generate", "cannot build capture", err)
	}
	if err := write(CaptureFile, capture); err != nil {
		return nil, err
	}
	if err := write(LegacyFile, []byte(BuildLegacy())); err != nil {
		return nil, err
	}

	// The plan covers itself: register the name before marshaling so the
	// on-disk plan and the in-memory input list agree.
	plan.Inputs = append(plan.Inputs, PlanFile)
	planData, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return nil, core.E(core.KindInput, "generate", "cannot encode plan", err)
	}
	if err := os.WriteFile(filepath.Join(work, PlanFile), planData, 0o644); err != nil {
		return nil, core.E(core.KindInput, "generate", "cannot write plan", err).At(PlanFile, -1)
	}
	return plan, nil
}

// LoadPlan reads the plan from a work directory.
func LoadPlan(work string) (*Plan, error) {
	data, err := os.ReadFile(filepath.Join(work, PlanFile))
	if err != nil {
		return nil, core.E(core.KindInput, "load-plan", "cannot read plan", err).At(work, -1)
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, core.E(core.KindInput, "load-plan", "corrupt plan", err).At(work, -1)
	}
	return &p, nil
}
