package scan

import (
	"fmt"

	"github.com/pion/rtp"

	"mediabatch/internal/fault"
)

// RTP dumps are length-prefixed: a 2-byte big-endian record length followed by
// the packet bytes. Bare RTP has no framing of its own, so a concatenated dump
// cannot be split reliably; the length prefix is what makes local captures
// unambiguous. IsRTPDump validates that the first record actually holds an
// RTP v2 packet, so an MP4 or arbitrary binary file never matches.
func IsRTPDump(data []byte) bool {
	if len(data) < 14 {
		return false
	}
	n := int(data[0])<<8 | int(data[1])
	if n < 12 || 2+n > len(data) {
		return false
	}
	return data[2]>>6 == 2
}

// inspectRTP validates a length-prefixed RTP packet dump with pion/rtp.
func inspectRTP(req Request, data []byte) (string, MediaInfo, fault.List) {
	var issues fault.List
	info := MediaInfo{Kind: "rtp"}
	off := 0
	var lastSeq uint16
	first := true
	for off < len(data) {
		if off+2 > len(data) {
			issues = append(issues, fault.Issue{
				Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
				Message: "RTP dump ends inside a record length prefix",
				Source: fault.Source{
					Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
					Loc:   fault.Locator{File: req.Path, Offset: int64(off)},
					Start: req.Start, End: req.End,
				},
			})
			break
		}
		n := int(data[off])<<8 | int(data[off+1])
		start := off + 2
		if n == 0 || start+n > len(data) {
			issues = append(issues, fault.Issue{
				Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
				Message: fmt.Sprintf("RTP record at offset %d declares %d bytes but only %d remain",
					off, n, len(data)-start),
				Source: fault.Source{
					Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
					Loc:   fault.Locator{File: req.Path, Offset: int64(off)},
					Start: req.Start, End: req.End,
				},
			})
			break
		}
		var p rtp.Packet
		if err := p.Unmarshal(data[start : start+n]); err != nil {
			issues = append(issues, fault.Issue{
				Code: fault.CodeSegmentTruncated, Severity: fault.SeverityError, Action: fault.ActionIsolated,
				Message: fmt.Sprintf("RTP packet at offset %d is corrupt or truncated: %v", start, err),
				Source: fault.Source{
					Manifest: req.Manifest, Representation: req.Representation, Segment: req.Ref,
					Loc:   fault.Locator{File: req.Path, Offset: int64(start)},
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
		// Advance by the declared record length, never by the parsed packet
		// size: padding and extensions make the latter unreliable for framing.
		off = start + n
	}
	return "rtp", info, issues
}
