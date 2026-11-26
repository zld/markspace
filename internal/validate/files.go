package validate

import (
	"fmt"
	"os"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
	"mediasync/internal/stream"
	"mediasync/internal/subtitle"
)

// Files lists optional local inputs for the check adapter.
type Files struct {
	Name     string
	Window   clock.Tick
	MP4      string
	SRT      string
	VTT      string
	RTP      string
	Timecode string
}

// FromFiles loads and analyses the named files. Read failures are returned as
// errors (execution failure); parse failures become reported issues.
func FromFiles(f Files) (*Result, error) {
	var cues []subtitle.Track
	var cueIssues issue.List
	if f.SRT != "" {
		data, err := os.ReadFile(f.SRT)
		if err != nil {
			return nil, fmt.Errorf("read SRT: %w", err)
		}
		tr, issues := subtitle.ParseSRT(f.SRT, string(data))
		cues = append(cues, tr)
		cueIssues.Add(issues...)
	}
	if f.VTT != "" {
		data, err := os.ReadFile(f.VTT)
		if err != nil {
			return nil, fmt.Errorf("read VTT: %w", err)
		}
		tr, issues := subtitle.ParseVTT(f.VTT, string(data))
		cues = append(cues, tr)
		cueIssues.Add(issues...)
	}
	if f.Timecode != "" {
		data, err := os.ReadFile(f.Timecode)
		if err != nil {
			return nil, fmt.Errorf("read timecode: %w", err)
		}
		_, issues := clock.ParseTimecodeFile(f.Timecode, string(data))
		cueIssues.Add(issues...)
	}
	var mp4 *stream.MP4Result
	if f.MP4 != "" {
		r, err := stream.ParseMP4(f.MP4)
		if err != nil {
			return nil, fmt.Errorf("read MP4: %w", err)
		}
		mp4 = r
	}
	var rtp *stream.RTPSession
	if f.RTP != "" {
		r, err := stream.ParseRTPFile(f.RTP, 90000)
		if err != nil {
			return nil, fmt.Errorf("read RTP: %w", err)
		}
		rtp = r
	}
	name := f.Name
	if name == "" {
		name = "check"
	}
	return Analyze(Spec{Name: name, Window: f.Window, Cues: cues, CueIssues: cueIssues, MP4: mp4, RTP: rtp}), nil
}
