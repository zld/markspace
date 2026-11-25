package pipeline

import (
	"errors"
	"sort"
	"strconv"

	"mediapack/internal/clock"
	"mediapack/internal/fault"
	"mediapack/internal/index"
	"mediapack/internal/report"
	"mediapack/internal/stream"
)

func decisionList(plan index.Plan) []index.Decision {
	out := make([]index.Decision, 0, len(plan.Reused)+len(plan.Rebuilt))
	out = append(out, plan.Reused...)
	out = append(out, plan.Rebuilt...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func decisionMap(plan index.Plan) map[string]index.Decision {
	out := map[string]index.Decision{}
	for _, d := range plan.Reused {
		out[d.ID] = d
	}
	for _, d := range plan.Rebuilt {
		out[d.ID] = d
	}
	return out
}

func removeDecision(in []index.Decision, id string) []index.Decision {
	out := in[:0]
	for _, d := range in {
		if d.ID != id {
			out = append(out, d)
		}
	}
	return out
}

func failureReport(err error) *report.FailureReport {
	code := fault.ExitCode(err)
	var fe *fault.Error
	if !errors.As(err, &fe) {
		return &report.FailureReport{Kind: string(fault.Unknown), Message: err.Error(), ExitCode: code}
	}
	fr := &report.FailureReport{
		Kind:     string(fe.Kind),
		Message:  fe.Message,
		Layer:    fe.Layer,
		Segment:  fe.Segment,
		Offset:   fe.Offset,
		Source:   fe.Source,
		ExitCode: code,
	}
	if fe.Kind == fault.BoundaryMismatch {
		fr.OffsetMS = clock.Ticks(fe.Offset).Milliseconds()
	}
	return fr
}

func hashCollections(statuses []report.LayerStatus) (string, []string, map[string]string) {
	byID := map[string]string{}
	var flat []string
	var ids []string
	for _, ls := range statuses {
		for _, sh := range ls.SegmentHashes {
			id := ls.Name + "#" + strconv.Itoa(sh.Index+1)
			byID[id] = sh.SHA256
			flat = append(flat, sh.SHA256)
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	ordered := make([]string, 0, len(ids))
	h := index.SHA256Hex([]byte(joinOrdered(ids, byID)))
	for _, id := range ids {
		ordered = append(ordered, byID[id])
	}
	return h, ordered, byID
}

func joinOrdered(ids []string, byID map[string]string) string {
	var b []byte
	for _, id := range ids {
		b = append(b, id+":"+byID[id]+"\n"...)
	}
	return string(b)
}

func reusedLayerFromRecord(layer string, rec index.Record, windows []stream.Window) (report.LayerStatus, []stream.Segment) {
	keys := make([]int, 0, len(rec.SegmentHashes))
	for k := range rec.SegmentHashes {
		if n, err := strconv.Atoi(k); err == nil {
			keys = append(keys, n)
		}
	}
	sort.Ints(keys)
	st := report.LayerStatus{Name: layer, Status: "reused", LastCompleteIndex: -1, SegmentCount: len(windows)}
	var segs []stream.Segment
	for _, idx := range keys {
		hash := rec.SegmentHashes[strconv.Itoa(idx)]
		st.SegmentHashes = append(st.SegmentHashes, report.SegmentHash{Layer: layer, Index: idx, SHA256: hash})
		st.LastCompleteIndex = idx
		if idx < len(windows) {
			segs = append(segs, stream.Segment{
				Layer: layer, Index: idx, Start: windows[idx].Start, End: windows[idx].End, SHA256: hash,
			})
		}
	}
	return st, segs
}

func statusFromSegments(layer, status string, segments []builtSegment, restored map[int]string) report.LayerStatus {
	st := report.LayerStatus{Name: layer, Status: status, LastCompleteIndex: -1, SegmentCount: SegmentCount}
	for idx, hash := range restored {
		st.SegmentHashes = append(st.SegmentHashes, report.SegmentHash{Layer: layer, Index: idx, SHA256: hash})
		if idx > st.LastCompleteIndex {
			st.LastCompleteIndex = idx
		}
	}
	for _, bs := range segments {
		st.SegmentHashes = append(st.SegmentHashes, report.SegmentHash{Layer: layer, Index: bs.segment.Index, SHA256: bs.hash})
		if bs.segment.Index > st.LastCompleteIndex {
			st.LastCompleteIndex = bs.segment.Index
		}
	}
	sort.Slice(st.SegmentHashes, func(i, j int) bool { return st.SegmentHashes[i].Index < st.SegmentHashes[j].Index })
	return st
}
