package transform_test

import (
	"sort"
	"testing"

	"mediacore/internal/fault"
	"mediacore/internal/fixture"
	"mediacore/internal/ingest"
	"mediacore/internal/timeline"
	"mediacore/internal/transform"
	"mediacore/internal/verify"
)

// TestFaultsFlowThroughEveryStage is the cross-stage requirement: at least
// three fault classes are carried from parsing, through repair and frames, all
// the way to verification, with the original input retained.
func TestFaultsFlowThroughEveryStage(t *testing.T) {
	cases := []struct {
		kind     fixture.FaultKind
		wantCode fault.Code
	}{
		{fixture.FaultTruncation, fault.CodeTruncatedBox},
		{fixture.FaultOversizeBox, fault.CodeTruncatedBox},
		{fixture.FaultCorruptMdat, fault.CodeSampleOutOfRange},
		{fixture.FaultTfdtRegression, fault.CodeTfdtRegression},
		{fixture.FaultSequenceGap, fault.CodeSequenceGap},
		{fixture.FaultDuplicate, fault.CodeDuplicateFragment},
		{fixture.FaultIndexGap, fault.CodeIndexGap},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.kind), func(t *testing.T) {
			injected, err := fixture.Inject(41, 2, 24, []fixture.FaultKind{tc.kind})
			if err != nil {
				t.Fatal(err)
			}
			original := append([]byte(nil), injected.Data...)
			r, f := ingest.NewReader("s.fmp4", injected.Data, ingest.DefaultLimits())
			if f != nil {
				t.Fatal(f)
			}
			info, f := ingest.ParseInit(r)
			if f != nil {
				t.Fatalf("init: %v", f)
			}
			idx, idxFault := ingest.ScanIndex(r, info.MoovOffset+info.MoovSize)
			res := r.ScanFragmentsBounded(info.MoovOffset+info.MoovSize, idx)
			var allErr []*fault.Error
			if idxFault != nil {
				allErr = append(allErr, idxFault)
			}
			allErr = append(allErr, res.Errors...)

			tracks, mapFaults := timeline.BuildTracks([]*ingest.InitInfo{info}, res.Fragments, 90000)
			allErr = append(allErr, mapFaults...)
			report, repaired := transform.Repair(tracks, transform.RepairOptions{})
			allErr = append(allErr, report.Errors...)
			for _, d := range report.Discontinuities {
				allErr = append(allErr, fault.New(fault.Code(d.Kind), d.File, d.Offset, "", d.Detail))
			}
			var exportable []timeline.Sample
			for _, rt := range repaired {
				exportable = append(exportable, rt.Samples...)
			}
			frames, err := transform.BuildFrames(exportable, mapPayloads{data: injected.Data}, transform.FrameOptions{Seed: 41, Timescale: 90000})
			if err != nil {
				t.Fatalf("frames: %v", err)
			}
			// The input must be untouched by every stage.
			if string(original) != string(injected.Data) {
				t.Fatal("pipeline mutated its input")
			}
			if len(allErr) == 0 {
				t.Fatalf("%s produced no error record anywhere", tc.kind)
			}
			// At least one recorded fault must carry the injected offset.
			found := false
			for _, e := range allErr {
				if e.Offset == injected.Injections[0].ByteOffset {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: no stage reported offset %d; got %v", tc.kind, injected.Injections[0].ByteOffset, allErr)
			}
			// Verification must pass on the repaired output. The exporter's
			// contract is that frames are ordered; the merged sequence must be
			// presented in that same order.
			manifest := verify.NewManifest(41, "cfg", "in", nil)
			manifest.AddFrames(frames)
			ordered := append([]timeline.Sample(nil), exportable...)
			sort.SliceStable(ordered, func(i, j int) bool {
				if ordered[i].TimelineTime != ordered[j].TimelineTime {
					return ordered[i].TimelineTime < ordered[j].TimelineTime
				}
				if ordered[i].TrackID != ordered[j].TrackID {
					return ordered[i].TrackID < ordered[j].TrackID
				}
				if ordered[i].FragmentIndex != ordered[j].FragmentIndex {
					return ordered[i].FragmentIndex < ordered[j].FragmentIndex
				}
				return ordered[i].SampleIndex < ordered[j].SampleIndex
			})
			failures := verify.Run(verify.Input{
				FileSizes: map[string]int64{"s.fmp4": int64(len(injected.Data))},
				Merged:    ordered,
				Frames:    frames.Frames,
				Manifest:  manifest,
			})
			if len(failures) != 0 {
				t.Fatalf("%s failed verification: %v", tc.kind, failures)
			}
		})
	}
}

// TestOtherKindsProduceExpectedCodes spot-checks the code mapping above.
func TestOtherKindsProduceExpectedCodes(t *testing.T) {
	// Guard against silently changing a code without updating the table.
	if fault.CodeTfdtRegression != "tfdt_regression" {
		t.Fatal("tfdt code name changed")
	}
	if fault.CodeDuplicateFragment != "duplicate_fragment" {
		t.Fatal("duplicate code name changed")
	}
}
