package pipeline

import (
	"encoding/json"
	"fmt"

	"mediascan/internal/fault"
	"mediascan/internal/report"
	"mediascan/internal/stream"
)

// ValidateStage re-parses both the transform artifact and the original
// bytes and compares structure fields and the time line. Any divergence
// becomes a validate-stage fault; the segment result is marked so the
// driver can exit non-zero.
func (p *Pipeline) ValidateStage(ps *ParsedSegment, ns *NormalizedSegment, outputHash string) bool {
	in := ps.Input
	if ps.Damaged {
		// Damaged inputs are expected to fail; they are already isolated,
		// so validation only re-confirms the boundary is still locatable.
		return ps.MP4 == nil || ps.MP4.UnsafeBoundary >= 0
	}

	// 1. Determinism: canonicalizing twice must reproduce the same hash.
	raw, err := Canonicalize(ns)
	if err != nil {
		p.addValidateFault(in, fault.KindRoundtripMismatch, err.Error())
		return false
	}
	if reportHash := report.Sum256(raw); reportHash != outputHash {
		p.addValidateFault(in, fault.KindHashMismatch,
			fmt.Sprintf("output hash unstable: %s != %s", reportHash, outputHash))
		return false
	}

	// 2. Round trip of the normalized structure itself.
	var again NormalizedSegment
	if err := json.Unmarshal(raw, &again); err != nil {
		p.addValidateFault(in, fault.KindRoundtripMismatch, err.Error())
		return false
	}
	if !sameStructure(ns, &again) {
		p.addValidateFault(in, fault.KindRoundtripMismatch,
			"normalized structure changed across re-parse")
		return false
	}

	// 3. Independent re-parse of the original bytes.
	reparsed, err := stream.ParseMP4(in.Data)
	if err != nil {
		p.addValidateFault(in, fault.KindRoundtripMismatch,
			"clean input failed re-parse: "+err.Error())
		return false
	}
	if reparsed.MediaSeq != ns.MediaSeqBox || reparsed.BaseDTS != ns.BaseDTS {
		p.addValidateFault(in, fault.KindRoundtripMismatch,
			fmt.Sprintf("header fields differ: seq %d!=%d dts %d!=%d",
				reparsed.MediaSeq, ns.MediaSeqBox, reparsed.BaseDTS, ns.BaseDTS))
		return false
	}
	if len(reparsed.Samples) != len(ns.Samples) {
		p.addValidateFault(in, fault.KindRoundtripMismatch,
			fmt.Sprintf("sample count differs: %d != %d",
				len(reparsed.Samples), len(ns.Samples)))
		return false
	}

	// 4. Library-level structural cross-check.
	if _, err := stream.ValidateWithLibrary(in.Data); err != nil {
		p.addValidateFault(in, fault.KindRoundtripMismatch,
			"go-mp4 cross-check rejected segment: "+err.Error())
		return false
	}

	// 5. Time line monotonicity (negative jumps must never appear).
	var prev float64
	for i, s := range ns.Samples {
		if i > 0 && s.TimeSeconds+1e-12 < prev {
			p.addValidateFault(in, fault.KindTimestampJump,
				fmt.Sprintf("normalized time moved backwards at sample %d: %f < %f",
					s.Index, s.TimeSeconds, prev))
			return false
		}
		prev = s.TimeSeconds
	}
	return true
}

func (p *Pipeline) addValidateFault(in SegmentInput, kind fault.Kind, msg string) {
	p.Log.Add(fault.Record{
		Stage: fault.StageValidate, Severity: fault.SeverityError,
		Kind: kind, Segment: in.Name, MediaSeq: in.MediaSeq,
		Message: msg,
	})
}

func sameStructure(a, b *NormalizedSegment) bool {
	if a.MediaSeq != b.MediaSeq || a.Timescale != b.Timescale ||
		a.BaseDTS != b.BaseDTS || len(a.Boxes) != len(b.Boxes) ||
		len(a.Samples) != len(b.Samples) {
		return false
	}
	for i := range a.Boxes {
		if a.Boxes[i] != b.Boxes[i] {
			return false
		}
	}
	for i := range a.Samples {
		if a.Samples[i] != b.Samples[i] {
			return false
		}
	}
	return true
}
