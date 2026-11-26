package pipeline

import (
	"bufio"
	"context"
	"encoding/json"
	"os"

	"pwflow/internal/ebml"
	"pwflow/internal/fault"
	"pwflow/internal/planner"
	pwrtp "pwflow/internal/rtp"
	"pwflow/internal/segment"
	"pwflow/internal/timebase"
)

// Options configures one pipeline run.
type Options struct {
	Input      string
	Events     string
	RTP        string
	Out        string
	Checkpoint string
	BudgetMiB  int64
	Workers    int
	StopAfter  int
	RTPClockHz int
	FrameFPS   int
	SampleRate int
	Context    context.Context
}

type inputBundle struct {
	digest      InputDigest
	mediaBytes  []byte
	ebml        *ebml.Report
	rtpRecords  []pwrtp.Captured
	repaired    pwrtp.Repaired
	bandwidth   *planner.BandwidthTracker
	events      []segment.Event
	parseErrors []fault.Record
}

type eventLine struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	AtNS   int64  `json:"at_ns"`
	TimeNS int64  `json:"time_ns"`
}

func loadEvents(path string) ([]segment.Event, []fault.Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fault.Wrap(fault.InvalidArgument, err)
	}
	defer f.Close()
	var events []segment.Event
	var records []fault.Record
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 0, 4096), 1<<20)
	lineNo := 0
	for s.Scan() {
		lineNo++
		line := s.Bytes()
		if len(line) == 0 {
			continue
		}
		var in eventLine
		if err := json.Unmarshal(line, &in); err != nil {
			records = append(records, fault.Record{
				Code: fault.TimeUnmappable, Offset: int64(lineNo),
				Message: "invalid event JSON: " + err.Error(),
			})
			continue
		}
		at := in.AtNS
		if at == 0 && in.TimeNS != 0 {
			at = in.TimeNS
		}
		if at < 0 {
			records = append(records, fault.Record{
				Code: fault.TimeUnmappable, Offset: int64(lineNo),
				Message: "negative event timestamp was not moved: " + in.ID,
			})
			continue
		}
		events = append(events, segment.Event{ID: in.ID, Kind: in.Kind, AtNS: at})
	}
	if err := s.Err(); err != nil {
		return events, records, fault.Wrap(fault.InvalidArgument, err)
	}
	return events, records, nil
}

func readInputs(opts Options) (*inputBundle, error) {
	if opts.Input == "" || opts.Events == "" || opts.RTP == "" || opts.Out == "" {
		return nil, fault.New(fault.InvalidArgument, "--input, --events, --rtp and --out are required")
	}
	if opts.BudgetMiB <= 0 {
		opts.BudgetMiB = 64
	}
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	clockHz := opts.RTPClockHz
	if clockHz <= 0 {
		clockHz = 48000
	}

	if st, err := os.Stat(opts.Input); err != nil {
		return nil, fault.Wrap(fault.InvalidArgument, err)
	} else if st.Size() > ebml.DefaultMaxBytes {
		return nil, fault.New(fault.InputTooLarge, "media is %d bytes; limit is %d", st.Size(), ebml.DefaultMaxBytes)
	}
	digest, err := DigestFiles(map[string]string{
		"media": opts.Input, "events": opts.Events, "rtp": opts.RTP,
	})
	if err != nil {
		return nil, fault.Wrap(fault.InvalidArgument, err)
	}
	media, err := os.ReadFile(opts.Input)
	if err != nil {
		return nil, fault.Wrap(fault.InvalidArgument, err)
	}
	if int64(len(media)) > ebml.DefaultMaxBytes {
		return nil, fault.New(fault.InputTooLarge, "media is %d bytes; limit is %d", len(media), ebml.DefaultMaxBytes)
	}
	lim := ebml.DefaultLimits()
	ebmlReport, ebmlErr := ebml.Parse(media, lim)
	if ebmlErr != nil {
		return nil, ebmlErr
	}

	rtpFile, err := os.Open(opts.RTP)
	if err != nil {
		return nil, fault.Wrap(fault.InvalidArgument, err)
	}
	records, err := pwrtp.ReadTrace(rtpFile)
	rtpFile.Close()
	if err != nil {
		return nil, fault.Wrap(fault.InvalidArgument, err)
	}
	repairer := pwrtp.NewRepairer(64)
	bw := &planner.BandwidthTracker{Alpha: 0.3}
	for _, rec := range records {
		repairer.Add(rec)
		frameBytes := len(rec.Packet.Payload) + 12
		bw.ObserveArrival(rec.ArrivalNS, frameBytes)
	}
	repaired := repairer.Flush()
	scale := timebase.Rational{Num: 1, Den: int64(clockHz)}
	repaired.Gaps = pwrtp.AssignGapIntervals(repaired.Gaps, repaired.Ordered, 960,
		func(ts uint32) (int64, error) { return scale.ScaleToNano(int64(ts)) })

	events, parseErrs, err := loadEvents(opts.Events)
	if err != nil {
		return nil, err
	}
	return &inputBundle{
		digest: digest, mediaBytes: media, ebml: ebmlReport,
		rtpRecords: records, repaired: repaired, bandwidth: bw,
		events: events, parseErrors: parseErrs,
	}, nil
}

func keyframesFromEBML(r *ebml.Report) []int64 {
	var out []int64
	for _, b := range r.Blocks {
		if b.TrackNumber == videoTrackNumber(r) && b.Keyframe && b.PTSNS >= 0 {
			out = append(out, b.PTSNS)
		}
	}
	return out
}

func videoTrackNumber(r *ebml.Report) uint64 {
	for _, t := range r.Tracks {
		if t.Type == 1 {
			return t.Number
		}
	}
	return 1
}

func videoDimensions(r *ebml.Report) (int, int) {
	for _, t := range r.Tracks {
		if t.Type == 1 && t.Width > 0 && t.Height > 0 {
			return int(t.Width), int(t.Height)
		}
	}
	return 320, 240
}
