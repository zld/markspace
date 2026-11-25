package selfcheck

import (
	"fmt"
	"io"
	"strings"

	"github.com/zld/mediacore/internal/pipeline"
)

// CaseReport is the stable terminal report for one generated sample.
type CaseReport struct {
	Name             string
	OK               bool
	SegmentCount     int
	Result           *pipeline.Result
	TimeDeviationNS  int64
	ByteDeviation    int64
	InjectedOffset   int64
	FailureOffset    int64
	HasFailureOffset bool
	Problems         []string
}

func printCase(w io.Writer, r CaseReport) {
	status := "PASS"
	if !r.OK {
		status = "FAIL"
	}
	fmt.Fprintf(w, "=== sample %s [%s]\n", r.Name, status)
	fmt.Fprintf(w, "sample=%s segment_count=%d\n", r.Name, r.SegmentCount)
	if r.Result != nil {
		for _, seg := range r.Result.Segments {
			for _, src := range seg.Sources {
				fmt.Fprintf(w,
					"mapping segment=%d transition=%t source_track=%d start_ns=%d end_ns=%d local_start_ns=%d local_end_ns=%d byte_start=%d byte_end=%d samples=%v\n",
					seg.Index, seg.Transition, src.TrackID,
					src.Timeline.Start, src.Timeline.End,
					src.Local.Start, src.Local.End,
					src.ByteRange.Start, src.ByteRange.End,
					src.SampleIndexes,
				)
			}
		}
		fmt.Fprintf(w, "silence_intervals=%d duplicate_groups=%d\n", len(r.Result.Silence), len(r.Result.Duplicates))
		for _, d := range r.Result.Duplicates {
			fmt.Fprintf(w, "duplicate digest=%s keys=%s\n", d.Digest[:16], strings.Join(d.Keys, ","))
		}
	}
	fmt.Fprintf(w, "time_deviation_ns=%d\n", r.TimeDeviationNS)
	fmt.Fprintf(w, "injected_byte_offset=%d byte_deviation=%d\n", r.InjectedOffset, r.ByteDeviation)
	if r.Result != nil && r.Result.Failure != nil {
		f := r.Result.Failure
		off := int64(-1)
		if f.HasByteOffset {
			off = f.ByteOffset
		}
		fmt.Fprintf(w, "failure_code=%s failure_stage=%s failure_offset=%d track=%d time_ns=%d\n",
			f.Code, f.Stage, off, f.TrackID, f.TimeNS)
	} else {
		fmt.Fprintln(w, "failure_code=- failure_stage=- failure_offset=- track=- time_ns=-")
	}
	if r.Result != nil {
		fmt.Fprintf(w, "source_digest=%s\n", r.Result.SourceDigest)
		fmt.Fprintf(w, "derived_digest=%s\n", r.Result.DerivedDigest)
		fmt.Fprintf(w, "mapping_digest=%s\n", r.Result.MappingDigest)
	}
	for _, p := range r.Problems {
		fmt.Fprintf(w, "assertion_error=%s\n", p)
	}
}
