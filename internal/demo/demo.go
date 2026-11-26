// Package demo assembles the acceptance scenarios: a normal sample, a
// truncated MP4, a corrupted RTP recording, an illegal drop-frame timecode,
// and in-window / out-of-window late subtitle events. Every fixture is
// generated at run time from the seed; nothing is read from the network.
package demo

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"mediasync/internal/clock"
	"mediasync/internal/fixtures"
	"mediasync/internal/issue"
	"mediasync/internal/report"
	"mediasync/internal/stream"
	"mediasync/internal/subtitle"
	"mediasync/internal/timeline"
	"mediasync/internal/validate"
)

// Sample is one analysed scenario.
type Sample struct {
	Name   string
	Result *validate.Result
	Report string
	Digest string
	// BaselineDigest is the digest of the same inputs delivered in canonical
	// event-time order. It is the reference for late-event scenarios.
	BaselineDigest string
}

// Run builds and analyses all scenarios. A returned error means the demo
// itself could not run (I/O), not that the material is bad.
func Run(seed int64, lateWindow clock.Tick) ([]Sample, error) {
	dir, err := os.MkdirTemp("", "mediasync-demo-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	var samples []Sample
	add := func(name string, res *validate.Result, baseline string) {
		rep := report.Render(res)
		samples = append(samples, Sample{
			Name: name, Result: res, Report: rep,
			Digest: report.Digest(report.BuildSnapshot(res)), BaselineDigest: baseline,
		})
	}
	digestOf := func(res *validate.Result) string {
		return report.Digest(report.BuildSnapshot(res))
	}

	// --- normal ---------------------------------------------------------
	normalMP4 := filepath.Join(dir, "normal.mp4")
	spec := defaultMP4Spec(seed)
	if _, err := fixtures.WriteMP4(normalMP4, spec, seed); err != nil {
		return nil, err
	}
	normalSRT := filepath.Join(dir, "normal.srt")
	if err := os.WriteFile(normalSRT, []byte(normalCues()), 0o644); err != nil {
		return nil, err
	}
	normalRTP := filepath.Join(dir, "normal.rtp")
	if _, err := fixtures.WriteRTPTimestamps(normalRTP, 90000, frameTimestamps(spec), seed); err != nil {
		return nil, err
	}
	srt, srtIssues := parseSRTFixture(normalSRT, "normal.srt")
	{
		normRes := validate.Analyze(validate.Spec{
			Name: "normal", Window: lateWindow,
			Cues: []subtitle.Track{srt}, CueIssues: srtIssues,
			MP4: mustMP4(normalMP4), RTP: mustRTP(normalRTP),
		})
		add("normal", normRes, digestOf(normRes))
	}

	// --- truncated MP4 --------------------------------------------------
	truncMP4 := filepath.Join(dir, "truncated.mp4")
	layout, err := fixtures.WriteMP4(truncMP4, spec, seed)
	if err != nil {
		return nil, err
	}
	if _, err := fixtures.TruncateAt(truncMP4, layout.StszOffset+10); err != nil {
		return nil, err
	}
	{
		res := validate.Analyze(validate.Spec{
			Name: "truncated-mp4", Window: lateWindow, MP4: mustMP4(truncMP4),
		})
		add("truncated-mp4", res, digestOf(res))
	}

	// --- corrupted RTP --------------------------------------------------
	badRTP := filepath.Join(dir, "corrupt.rtp")
	rtpLayout, err := fixtures.WriteRTPTimestamps(badRTP, 90000, frameTimestamps(spec), seed)
	if err != nil {
		return nil, err
	}
	target := rtpLayout.PayloadAt[len(rtpLayout.PayloadAt)/2]
	if _, err := fixtures.CorruptByte(badRTP, target, 0x40); err != nil {
		return nil, err
	}
	{
		res := validate.Analyze(validate.Spec{
			Name: "corrupt-rtp", Window: lateWindow, RTP: mustRTP(badRTP),
		})
		add("corrupt-rtp", res, digestOf(res))
	}

	// --- illegal drop-frame timecodes -----------------------------------
	tcText := timecodeLines()
	entries, tcIssues := clock.ParseTimecodeFile("timecodes.txt", tcText)
	{
		res := &validate.Result{
			Name: "illegal-df", Window: lateWindow, Issues: tcIssues,
			Sources: []string{"timecodes.txt"},
		}
		for _, e := range entries {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"line %d: %s %s -> ticks=%d frame_error=%d",
				e.Line, e.Rate, e.Text, int64(e.Ticks), e.FrameDiff))
		}
		add("illegal-df", res, digestOf(res))
	}

	// --- in-window late cue ---------------------------------------------
	baseline := validate.Analyze(validate.Spec{
		Name: "late-in-window", Window: lateWindow,
		Cues: []subtitle.Track{srt}, CueIssues: srtIssues, MP4: mustMP4(normalMP4),
	})
	add("late-in-window", validate.Analyze(validate.Spec{
		Name: "late-in-window", Window: lateWindow,
		Cues: []subtitle.Track{srt}, CueIssues: srtIssues, MP4: mustMP4(normalMP4),
		Arrival: func(evs []timeline.Event) []timeline.Event { return arriveLate(evs, 1500) },
	}), digestOf(baseline))

	// --- out-of-window late cue -----------------------------------------
	{
		res := validate.Analyze(validate.Spec{
			Name: "late-out-of-window", Window: lateWindow,
			Cues: []subtitle.Track{srt}, CueIssues: srtIssues, MP4: mustMP4(normalMP4),
			Arrival: func(evs []timeline.Event) []timeline.Event { return arriveLate(evs, 5000) },
		})
		add("late-out-of-window", res, digestOf(res))
	}

	return samples, nil
}

func parseSRTFixture(path, label string) (subtitle.Track, issue.List) {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return subtitle.ParseSRT(label, string(data))
}

func mustMP4(path string) *stream.MP4Result {
	// Report under the base name so digests never contain the temporary
	// directory created for this run.
	res, err := stream.ParseMP4As(path, filepath.Base(path))
	if err != nil {
		panic(err)
	}
	return res
}

func mustRTP(path string) *stream.RTPSession {
	res, err := stream.ParseRTPFileAs(path, filepath.Base(path), 90000)
	if err != nil {
		panic(err)
	}
	return res
}

// arriveLate replays the event list with the first cue delivered late by
// delayMillis. Event times are never changed: only the arrival order is.
func arriveLate(evs []timeline.Event, delayMillis int) []timeline.Event {
	sorted := append([]timeline.Event(nil), evs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Less(sorted[j]) })
	idx := -1
	for i, e := range sorted {
		if e.Kind == timeline.KindCue {
			idx = i
			break
		}
	}
	if idx < 0 {
		return sorted
	}
	out := append([]timeline.Event(nil), sorted[:idx]...)
	out = append(out, sorted[idx+1:]...)
	late := sorted[idx]
	insert := len(out)
	for i, e := range out {
		if e.Time > late.Time+clock.FromMilliseconds(int64(delayMillis)) {
			insert = i
			break
		}
	}
	out = append(out, timeline.Event{})
	copy(out[insert+1:], out[insert:])
	out[insert] = late
	return out
}

func timecodeLines() string {
	return `# rate timecode
25 00:00:00:00
25 00:00:01:00
29.97df 00:00:59;29
29.97df 00:01:00;00
29.97df 00:01:00;02
29.97df 00:10:00;00
29.97ndf 00:00:00:01
24 01:00:00:00
50 00:00:00:49
30 00:00:00:01
25 00:00:00:25
`
}

func normalCues() string {
	return `1
00:00:00,000 --> 00:00:00,900
第一句对白 / line one

2
00:00:01,000 --> 00:00:01,900
第二句对白 / line two

3
00:00:02,000 --> 00:00:02,900
第三句对白 / line three

4
00:00:03,000 --> 00:00:03,900
第四句对白 / line four

5
00:00:04,000 --> 00:00:04,900
第五句对白 / line five
`
}

func defaultMP4Spec(seed int64) fixtures.MP4Spec {
	const fps = 25
	const timescale = 25000
	n := 125
	deltas := make([]uint32, n)
	sizes := make([]uint32, n)
	var kf []int
	for i := 0; i < n; i++ {
		deltas[i] = timescale / fps
		sizes[i] = uint32(180 + (i*37+int(seed)*13)%140)
		if i%25 == 0 {
			kf = append(kf, i+1)
		}
	}
	return fixtures.MP4Spec{
		Timescale: timescale, SampleDelta: deltas, SampleSizes: sizes,
		Keyframes: kf, Width: 1280, Height: 720,
	}
}

// frameTimestamps maps each frame's decode time onto the 90 kHz RTP clock.
func frameTimestamps(spec fixtures.MP4Spec) []uint32 {
	n := len(spec.SampleDelta)
	out := make([]uint32, n)
	var acc uint64
	for i, d := range spec.SampleDelta {
		out[i] = uint32(acc * 90000 / uint64(spec.Timescale))
		acc += uint64(d)
	}
	return out
}
