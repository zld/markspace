// Package selfcheck generates local fixtures and verifies the acceptance flow.
package selfcheck

import (
	"fmt"
	"io"

	"github.com/zld/mediacore/internal/audio"
	"github.com/zld/mediacore/internal/pipeline"
	"github.com/zld/mediacore/internal/samplegen"
	"github.com/zld/mediacore/internal/segment"
	"github.com/zld/mediacore/internal/store"
	"github.com/zld/mediacore/internal/trace"
)

type caseSpec struct {
	name             string
	data             []byte
	opts             pipeline.Options
	injectedOffset   int64
	wantCode         trace.Code
	wantStage        trace.Stage
	wantSegments     int
	checkMappings    bool
	checkContainment bool
}

func standardOptions() pipeline.Options {
	return pipeline.Options{
		Segment: segment.Params{
			Cuts: []segment.Cut{
				{At: 400_000_000, Label: "transition-start"},
				{At: 600_000_000, Label: "transition-end"},
			},
			Transitions: []segment.Transition{{
				ID: "xfade-1", Start: 400_000_000, End: 600_000_000, FromTrack: 1, ToTrack: 2,
			}},
		},
		Silence: audio.DefaultConfig(),
	}
}

// Run generates all samples, executes each twice, prints reports and returns
// a non-nil error when any acceptance assertion fails.
func Run(w io.Writer) error {
	cases, err := buildCases()
	if err != nil {
		return err
	}

	var problems []string
	var normalSource *store.Source
	var normalResult *pipeline.Result
	for _, tc := range cases {
		src := store.SourceFromBytes(tc.data, tc.name+".mp4")
		res, _ := pipeline.Process(src, tc.opts)
		rerun, _ := pipeline.Process(src, tc.opts)
		report := evaluate(tc, src, res, rerun)
		printCase(w, report)
		if !report.OK {
			problems = append(problems, report.Problems...)
		}
		if tc.name == "normal" {
			normalSource = src
			normalResult = res
		}
	}

	if normalSource == nil || normalResult == nil {
		return fmt.Errorf("normal sample did not produce a result")
	}
	paramProblems := checkParameterInvalidation(normalSource, normalResult)
	for _, p := range paramProblems {
		fmt.Fprintf(w, "parameter_check=FAIL reason=%s\n", p)
	}
	problems = append(problems, paramProblems...)

	if len(problems) > 0 {
		fmt.Fprintf(w, "SELF-CHECK FAIL: %d assertion(s)\n", len(problems))
		return fmt.Errorf("self-check failed: %d assertion(s)", len(problems))
	}
	fmt.Fprintln(w, "parameter_check=PASS source_digest_unchanged=true segment_key_stable=true audio_key_changed=true")
	fmt.Fprintln(w, "SELF-CHECK PASS")
	return nil
}

func buildCases() ([]caseSpec, error) {
	normal, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		return nil, err
	}
	truncated, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		return nil, err
	}
	truncOff, ok := samplegen.TruncateInSample(truncated, 2, 4, 32)
	if !ok {
		return nil, fmt.Errorf("inject truncation")
	}

	badLength, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		return nil, err
	}
	badOff, ok := samplegen.CorruptBoxLength(badLength, 2, 4)
	if !ok {
		return nil, fmt.Errorf("inject bad box length")
	}

	noIndex, err := samplegen.Generate(samplegen.NormalSpec(false))
	if err != nil {
		return nil, err
	}

	boundary, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		return nil, err
	}
	boundaryOff, ok := samplegen.TruncateInSample(boundary, 1, 5, 16)
	if !ok {
		return nil, fmt.Errorf("inject boundary truncation")
	}
	boundaryOpts := standardOptions()
	boundaryOpts.Segment.Cuts = append([]segment.Cut{}, boundaryOpts.Segment.Cuts...)
	boundaryOpts.Segment.Cuts = append(boundaryOpts.Segment.Cuts, segment.Cut{At: 500_000_000, Label: "damaged-boundary"})

	fpCorrupt, err := samplegen.Generate(samplegen.NormalSpec(true))
	if err != nil {
		return nil, err
	}
	flipOff, ok := samplegen.FlipFirstPayloadByte(fpCorrupt, 1, 4)
	if !ok {
		return nil, fmt.Errorf("inject payload flip")
	}
	fpOpts := standardOptions()
	fpOpts.VerifyPayloads = true

	return []caseSpec{
		{name: "normal", data: normal.Data, opts: standardOptions(), wantSegments: 3, checkMappings: true},
		{name: "truncated-box", data: truncated.Data, opts: standardOptions(), injectedOffset: truncOff,
			wantCode: trace.CodeTruncated, wantStage: trace.StageIngest, wantSegments: 3, checkContainment: true},
		{name: "bad-box-length", data: badLength.Data, opts: standardOptions(), injectedOffset: badOff,
			wantCode: trace.CodeBoxLengthOutOfBounds, wantStage: trace.StageIngest, wantSegments: 3, checkContainment: true},
		{name: "missing-index", data: noIndex.Data, opts: standardOptions(), wantSegments: 3, checkMappings: true},
		{name: "boundary-cut", data: boundary.Data, opts: boundaryOpts, injectedOffset: boundaryOff,
			wantCode: trace.CodeCutPointCorrupt, wantStage: trace.StageCut, wantSegments: 2, checkContainment: true},
		{name: "fingerprint-corrupt", data: fpCorrupt.Data, opts: fpOpts, injectedOffset: flipOff,
			wantCode: trace.CodePayloadMismatch, wantStage: trace.StageFingerprint, wantSegments: 3},
	}, nil
}

func checkParameterInvalidation(src *store.Source, normal *pipeline.Result) []string {
	changedOpts := standardOptions()
	changedOpts.Silence.MinDurationNS = 100_000_000 // still detects the 200ms fixture silence
	changed, err := pipeline.Process(src, changedOpts)
	if err != nil {
		return []string{fmt.Sprintf("changed-params process: %v", err)}
	}
	var problems []string
	if changed.SourceDigest != src.Digest {
		problems = append(problems, "source digest changed when only parameters changed")
	}
	if changed.DerivedDigest == normal.DerivedDigest {
		problems = append(problems, "derived digest did not change with processing parameters")
	}
	if changed.Keys["segment"].Key != normal.Keys["segment"].Key {
		problems = append(problems, "segment cache key changed after audio-only parameter change")
	}
	if changed.Keys["audio"].Key == normal.Keys["audio"].Key {
		problems = append(problems, "audio cache key did not change with audio parameter")
	}
	if changed.MappingDigest != normal.MappingDigest {
		problems = append(problems, "mapping content changed despite equivalent silence result")
	}
	return problems
}
