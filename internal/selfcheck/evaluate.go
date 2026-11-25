package selfcheck

import (
	"strconv"

	"github.com/zld/mediacore/internal/pipeline"
	"github.com/zld/mediacore/internal/trace"
)

func evaluate(tc caseSpec, _ interface{}, res, rerun *pipeline.Result) CaseReport {
	report := CaseReport{Name: tc.name, InjectedOffset: tc.injectedOffset, Result: res, OK: false}
	if res == nil {
		report.Problems = append(report.Problems, "pipeline returned nil result")
		return report
	}
	report.SegmentCount = len(res.Segments)
	report.TimeDeviationNS = res.ConversionDeviationNS

	if rerun == nil {
		report.Problems = append(report.Problems, "second run returned nil result")
	} else {
		if res.DerivedDigest != rerun.DerivedDigest {
			report.Problems = append(report.Problems, "derived digest differs on repeated run")
		}
		if res.MappingDigest != rerun.MappingDigest {
			report.Problems = append(report.Problems, "mapping digest differs on repeated run")
		}
		if failureLocation(res.Failure) != failureLocation(rerun.Failure) {
			report.Problems = append(report.Problems, "failure location differs on repeated run")
		}
	}

	if len(res.Segments) != tc.wantSegments {
		report.Problems = append(report.Problems, "unexpected segment count")
	}

	if tc.wantCode == "" {
		if res.Failure != nil {
			report.Problems = append(report.Problems, "unexpected failure: "+res.Failure.Error())
		}
	} else if res.Failure == nil {
		report.Problems = append(report.Problems, "expected failure but pipeline reported success")
	} else {
		f := res.Failure
		if f.Code != tc.wantCode {
			report.Problems = append(report.Problems, "failure code = "+string(f.Code)+", want "+string(tc.wantCode))
		}
		if f.Stage != tc.wantStage {
			report.Problems = append(report.Problems, "failure stage = "+string(f.Stage)+", want "+string(tc.wantStage))
		}
		if tc.wantTrack != 0 && (!f.HasTrack || f.TrackID != tc.wantTrack) {
			report.Problems = append(report.Problems, "failure track location mismatch")
		}
		if tc.injectedOffset >= 0 {
			if !f.HasByteOffset {
				report.Problems = append(report.Problems, "failure has no byte offset")
			} else {
				report.HasFailureOffset = true
				report.FailureOffset = f.ByteOffset
				report.ByteDeviation = f.ByteOffset - tc.injectedOffset
				if f.ByteOffset != tc.injectedOffset {
					report.Problems = append(report.Problems, "failure offset does not match injected corruption byte")
				}
			}
		}
	}

	if tc.checkMappings {
		report.Problems = append(report.Problems, checkStandardMappings(res)...)
	}
	if tc.checkContainment && tc.injectedOffset > 0 {
		for _, seg := range res.Segments {
			for _, src := range seg.Sources {
				if src.ByteRange.End > tc.injectedOffset {
					report.Problems = append(report.Problems, "mapping includes bytes after corruption point")
				}
			}
		}
	}
	if report.TimeDeviationNS != 0 {
		report.Problems = append(report.Problems, "time deviation exceeds aligned fixture expectation")
	}
	report.OK = len(report.Problems) == 0
	return report
}

func failureLocation(e *trace.Error) string {
	if e == nil {
		return ""
	}
	return string(e.Code) + "|" + string(e.Stage) + "|" +
		boolText(e.HasTrack) + ":" + intText(e.TrackID) + "|" +
		boolText(e.HasTime) + ":" + int64Text(e.TimeNS) + "|" +
		boolText(e.HasByteOffset) + ":" + int64Text(e.ByteOffset)
}

func boolText(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func intText(i int) string { return strconv.Itoa(i) }

func int64Text(i int64) string { return strconv.FormatInt(i, 10) }
