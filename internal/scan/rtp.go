package scan

import (
	"fmt"

	"github.com/pion/rtp"

	"mediabatch/internal/fault"
)

// IsRTPDump reports whether data begins with a plausible RTP v2 header. The
// second bit of the first byte carries the RTP version.
func IsRTPDump(data []byte) bool {
	return len(data) >= 12 && data[0]>>6 == 2
}

// inspectRTP validates a concatenated RTP packet dump with pion/rtp.
func inspectRTP(req Request, data []byte) (string, MediaInfo, fault.List) {
	var issues fault.List
	info := MediaInfo{Kind: "rtp"}
	off := 0
	var lastSeq uint16
	first := true
	for off < len(data) {
		var p rtp.Packet
		if err := p.Unmarshal(data[off:]); err != nil {
			issues = append(issues, fault.Issue{
				Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
				Message: fmt.Sprintf("RTP packet at offset %d is corrupt or truncated: %v", off, err),
				Source: fault.Source{
					Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
					Loc:   fault.Locator{File: req.Path, Offset: int64(off)},
					Start: req.Start, End: req.End,
				},
			})
			break
		}
		if p.Version != 2 {
			issues = append(issues, fault.Issue{
				Code: fault.CodeParse, Severity: fault.SeverityError, Action: fault.ActionIsolated,
				Message: fmt.Sprintf("RTP packet at offset %d declares version %d", off, p.Version),
				Source: fault.Source{
					Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
					Loc:   fault.Locator{File: req.Path, Offset: int64(off)},
					Start: req.Start, End: req.End,
				},
			})
		}
		if !first && p.SequenceNumber != lastSeq+1 {
			issues = append(issues, fault.Issue{
				Code: fault.CodeSegmentTruncated, Severity: fault.SeverityWarning, Action: fault.ActionRecorded,
				Message: fmt.Sprintf("RTP sequence gap %d -> %d", lastSeq, p.SequenceNumber),
				Source: fault.Source{
					Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
					Loc:   fault.Locator{File: req.Path, Offset: int64(off)},
					Start: req.Start, End: req.End,
				},
			})
		}
		lastSeq = p.SequenceNumber
		first = false
		info.SampleCount++
		info.PayloadBytes += int64(len(p.Payload))
		next := off + p.MarshalSize()
		if next <= off {
			break
		}
		off = next
	}
	return "rtp", info, issues
}
