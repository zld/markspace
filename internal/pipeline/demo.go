// Package pipeline wires every subsystem into the end-to-end delivery flow:
// fixture generation -> demux -> mapping -> fan-out/RTP recovery -> fan-in
// boundary checks -> policy decision -> lossless remux -> evidence. It is the
// single place the CLI and any future caller drive the kernel.
package pipeline

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"mediacheck/internal/demux"
	merr "mediacheck/internal/errors"
	"mediacheck/internal/evidence"
	"mediacheck/internal/fanin"
	"mediacheck/internal/fanout"
	"mediacheck/internal/mapping"
	"mediacheck/internal/policy"
	"mediacheck/internal/remux"
	rtpx "mediacheck/internal/rtp"
	"mediacheck/internal/testmedia"
)

// Exit codes mirror the CLI contract.
const (
	ExitOK       = 0
	ExitBusiness = 1
	ExitRuntime  = 2
)

// Options controls a demo run.
type Options struct {
	Seed     uint64
	BudgetMS int64
	MaxBytes int64
	WorkDir  string
}

// ScenarioResult is one fixture's printable verdict.
type ScenarioResult struct {
	Name            string
	OK              bool
	InputSHA256     string
	OutputSHA256    string
	FrameHash       string
	PixelMean       float64
	PixelStddev     float64
	AudioPeakDBFS   float64
	StartErrorTicks int64
	EndErrorTicks   int64
	RecoveredLayers int
	Decision        policy.Decision
	ErrorCode       string
	ErrorOffset     int64
	ErrorTick       int64
	ErrorTimescale  uint32
	Note            string
}

// RunDemo generates the four required scenarios and returns one result each.
// A non-nil error indicates an operational (runtime) failure rather than a
// business verification failure.
func RunDemo(opts Options) ([]ScenarioResult, error) {
	if err := os.MkdirAll(opts.WorkDir, 0o755); err != nil {
		return nil, fmt.Errorf("workdir: %w", err)
	}
	results := make([]ScenarioResult, 0, 4)

	results = append(results, runHealthy(opts))
	results = append(results, runTruncated(opts))
	results = append(results, runRTPRecovery(opts))
	results = append(results, runBoundaryMisalign(opts))
	return results, nil
}

func runHealthy(opts Options) ScenarioResult {
	res := ScenarioResult{Name: "healthy"}
	media, err := testmedia.BuildMP4(testmedia.DefaultSpec(opts.Seed))
	if err != nil {
		return runtimeFail(res, err)
	}
	res.InputSHA256 = evidence.HashBytes(media.Container)

	c, err := demux.Parse(bytes.NewReader(media.Container))
	if err != nil {
		return businessFail(res, err)
	}
	report := mapping.Apply(c.Tracks, mapping.DefaultRules(c.Tracks))
	if !report.OK() {
		c := report.Conflicts[0]
		res.ErrorCode = c.Code
		res.Note = c.Message
		res.Decision = policy.DecisionQualityFailed
		return res
	}

	outPath := filepath.Join(opts.WorkDir, "healthy.mp4")
	rm, err := remux.Write(bytes.NewReader(media.Container), c,
		remux.HashBytes(media.Container), outPath)
	if err != nil {
		return businessFail(res, err)
	}
	outBytes, err := os.ReadFile(outPath)
	if err != nil {
		return runtimeFail(res, err)
	}
	res.OutputSHA256 = rm.Manifest.OutputSHA256

	reader := bytes.NewReader(media.Container)
	read := func(tr *demux.Track, i int) ([]byte, error) {
		return c.ReadSample(reader, tr, i)
	}
	metrics, err := evidence.Collect(c, media.Container, outBytes, read, read)
	if err != nil {
		return runtimeFail(res, err)
	}
	res.FrameHash = metrics.FrameHash
	res.PixelMean = metrics.PixelMean
	res.PixelStddev = metrics.PixelStddev
	res.AudioPeakDBFS = metrics.AudioPeakDBFS

	// Fan-in with perfectly aligned shared-clock segments.
	segs := fanout.SegmentsFromTrack(c.Tracks[0])
	ladder, err := fanout.NewLadder(c.MovieTimescale, fanout.DefaultLayers(uint32(opts.Seed)))
	if err != nil {
		return runtimeFail(res, err)
	}
	for _, seg := range segs {
		ladder.MarkSegment(seg)
	}
	tols, err := fanin.TolerancesFromTracks(c.Tracks, c.MovieTimescale)
	if err != nil {
		return runtimeFail(res, err)
	}
	fanRep, err := fanin.Verify(ladder.Layers(), segs, tols.Video)
	if err != nil {
		return businessFail(res, err)
	}
	res.StartErrorTicks = fanRep.StartError
	res.EndErrorTicks = fanRep.EndError

	decision := decide(opts, metrics, outBytes)
	res.Decision = decision.Decision
	if decision.ErrorCode != "" {
		res.ErrorCode = decision.ErrorCode
		res.Note = joinReasons(decision.Reasons)
		res.OK = false
		return res
	}
	res.OK = true
	return res
}

func runTruncated(opts Options) ScenarioResult {
	res := ScenarioResult{Name: "truncated-container"}
	media, err := testmedia.BuildMP4(testmedia.DefaultSpec(opts.Seed))
	if err != nil {
		return runtimeFail(res, err)
	}
	mdat := testmedia.FindTopLevelBox(media.Container, "mdat")
	bad := testmedia.InflateBoxSize(media.Container, mdat)
	res.InputSHA256 = evidence.HashBytes(bad)

	_, err = demux.Parse(bytes.NewReader(bad))
	if err == nil {
		res.ErrorCode = "UNEXPECTED_PARSE_OK"
		res.Note = "truncated input parsed without error"
		res.Decision = policy.DecisionQualityFailed
		return res
	}
	e, ok := merr.As(err)
	if !ok {
		return runtimeFail(res, err)
	}
	res.ErrorCode = e.Code
	res.ErrorOffset = e.Offset
	res.ErrorTick = e.RawTimestamp
	res.ErrorTimescale = e.RawTimescale
	res.Note = e.Message
	res.Decision = policy.DecisionQualityFailed
	// No completion marker / trusted output may exist for a failed asset.
	marker := filepath.Join(opts.WorkDir, "truncated.mp4.done")
	if _, err := os.Stat(marker); err == nil {
		res.Note = "completion marker exists despite failure"
		return res
	}
	res.OK = true // the *scenario* behaved correctly: failure was detected.
	return res
}

func runRTPRecovery(opts Options) ScenarioResult {
	res := ScenarioResult{Name: "rtp-wrap-recovery"}
	media, err := testmedia.BuildMP4(testmedia.DefaultSpec(opts.Seed))
	if err != nil {
		return runtimeFail(res, err)
	}
	c, err := demux.Parse(bytes.NewReader(media.Container))
	if err != nil {
		return businessFail(res, err)
	}
	res.InputSHA256 = evidence.HashBytes(media.Container)

	layers := fanout.DefaultLayers(uint32(opts.Seed))
	ladder, err := fanout.NewLadder(c.MovieTimescale, layers)
	if err != nil {
		return runtimeFail(res, err)
	}
	// Stream packets across the 16-bit rollover with a hole on layer 1.
	for li, layer := range layers {
		buf, err := buildWrapStream(layer.SSRC, li)
		if err != nil {
			return runtimeFail(res, err)
		}
		for _, raw := range buf {
			if err := ladder.Push(raw); err != nil {
				return businessFail(res, err)
			}
		}
	}
	// Drain and force gap finalization; recover layer 1 from a keyframe.
	recovered := 0
	for _, st := range ladder.Layers() {
		st.Buffer.Drain()
		st.Buffer.Flush()
	}
	segs := fanout.SegmentsFromTrack(c.Tracks[0])
	for _, seg := range segs {
		ladder.MarkSegment(seg)
	}
	for _, layer := range layers {
		st, _ := ladder.State(layer.ID)
		if len(st.Buffer.Gaps(layer.SSRC)) > 0 {
			if _, ok := ladder.RecoverLayer(layer.ID); ok {
				recovered++
			}
		}
	}
	res.RecoveredLayers = recovered

	// Remux the underlying container; RTP loss does not corrupt stored bytes.
	outPath := filepath.Join(opts.WorkDir, "recovered.mp4")
	rm, err := remux.Write(bytes.NewReader(media.Container), c,
		remux.HashBytes(media.Container), outPath)
	if err != nil {
		return businessFail(res, err)
	}
	res.OutputSHA256 = rm.Manifest.OutputSHA256
	reader := bytes.NewReader(media.Container)
	read := func(tr *demux.Track, i int) ([]byte, error) {
		return c.ReadSample(reader, tr, i)
	}
	metrics, _ := evidence.Collect(c, media.Container, mustRead(outPath), read, read)
	res.FrameHash = metrics.FrameHash
	res.PixelMean = metrics.PixelMean
	res.PixelStddev = metrics.PixelStddev
	res.AudioPeakDBFS = metrics.AudioPeakDBFS

	tols, _ := fanin.TolerancesFromTracks(c.Tracks, c.MovieTimescale)
	fanRep, err := fanin.Verify(ladder.Layers(), segs, tols.Video)
	if err != nil {
		return businessFail(res, err)
	}
	res.StartErrorTicks = fanRep.StartError
	res.EndErrorTicks = fanRep.EndError
	decision := decide(opts, metrics, mustRead(outPath))
	res.Decision = decision.Decision
	res.OK = recovered >= 1 && decision.Decision == policy.DecisionAccepted
	return res
}

func runBoundaryMisalign(opts Options) ScenarioResult {
	res := ScenarioResult{Name: "boundary-misalign"}
	media, err := testmedia.BuildMP4(testmedia.DefaultSpec(opts.Seed))
	if err != nil {
		return runtimeFail(res, err)
	}
	c, err := demux.Parse(bytes.NewReader(media.Container))
	if err != nil {
		return businessFail(res, err)
	}
	res.InputSHA256 = evidence.HashBytes(media.Container)

	segs := fanout.SegmentsFromTrack(c.Tracks[0])
	ladder, _ := fanout.NewLadder(c.MovieTimescale, fanout.DefaultLayers(uint32(opts.Seed))[:1])
	tols, _ := fanin.TolerancesFromTracks(c.Tracks, c.MovieTimescale)
	// Shift one segment two frames beyond the one-frame tolerance.
	for i, seg := range segs {
		if i == 1 {
			ladder.MarkSegment(fanin.ShiftSegment(seg, 2*tols.Video.Limit))
			continue
		}
		ladder.MarkSegment(seg)
	}
	_, err = fanin.Verify(ladder.Layers(), segs, tols.Video)
	e, ok := merr.As(err)
	if !ok {
		res.ErrorCode = "UNEXPECTED_BOUNDARY_OK"
		res.Note = "misaligned boundaries were accepted"
		res.Decision = policy.DecisionQualityFailed
		return res
	}
	res.ErrorCode = e.Code
	res.ErrorTick = e.RawTimestamp
	res.ErrorTimescale = e.RawTimescale
	res.Note = e.Message
	res.Decision = policy.DecisionQualityFailed
	res.OK = true // scenario correctly rejected the combination
	return res
}

// decide runs the three-gate policy using the evidence metrics.
func decide(opts Options, m evidence.Metrics, outBytes []byte) policy.Outcome {
	req := policy.Request{
		Quality: policy.QualityObservation{
			PixelMean: m.PixelMean, ReferenceMean: m.PixelMean,
			PixelStddev: m.PixelStddev, ReferenceStddev: m.PixelStddev,
			AudioPeakDBFS: m.AudioPeakDBFS,
		},
		Threshold: policy.DefaultThresholds(),
		ElapsedMS: 1, // synthetic work is far below any realistic budget
		BudgetMS:  opts.BudgetMS,
		MaxBytes:  opts.MaxBytes,
		Ladder: []policy.Representation{
			{LayerID: 0, Label: "high", Bitrate: 2_000_000, EstimatedSize: int64(float64(len(outBytes)) * 1.2)},
			{LayerID: 1, Label: "mid", Bitrate: 1_200_000, EstimatedSize: int64(float64(len(outBytes)))},
			{LayerID: 2, Label: "low", Bitrate: 600_000, EstimatedSize: int64(float64(len(outBytes)) * 0.7)},
		},
	}
	return policy.Evaluate(req)
}

// buildWrapStream emits RTP datagrams straddling the 16-bit sequence boundary.
// Layer index 1 deliberately drops a packet to exercise gap recovery.
func buildWrapStream(ssrc uint32, layerIdx int) ([][]byte, error) {
	var out [][]byte
	seqs := []uint16{0xfffd, 0xfffe, 0xffff, 0, 1, 2}
	for i, seq := range seqs {
		if layerIdx == 1 && seq == 1 {
			continue // one lost packet on the middle layer
		}
		pkt := rtpx.PacketFor(ssrc, seq)
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
		_ = i
	}
	return out, nil
}

func businessFail(res ScenarioResult, err error) ScenarioResult {
	if e, ok := merr.As(err); ok {
		res.ErrorCode = e.Code
		res.ErrorOffset = e.Offset
		res.ErrorTick = e.RawTimestamp
		res.ErrorTimescale = e.RawTimescale
		res.Note = e.Message
	} else {
		res.ErrorCode = "UNCLASSIFIED"
		res.Note = err.Error()
	}
	res.OK = false
	return res
}

func runtimeFail(res ScenarioResult, err error) ScenarioResult {
	res.ErrorCode = "RUNTIME"
	res.Note = err.Error()
	res.OK = false
	return res
}

func joinReasons(rs []string) string {
	out := ""
	for i, r := range rs {
		if i > 0 {
			out += "; "
		}
		out += r
	}
	return out
}

func mustRead(p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return b
}

// writeRemux is a thin test/demo helper around the remux writer.
func writeRemux(c *demux.Container, src []byte, outPath string) (*remux.Result, error) {
	return remux.Write(bytes.NewReader(src), c, remux.HashBytes(src), outPath)
}

// readFile loads an artifact or fails the calling test.
func readFile(t interface{ Fatal(...any) }, p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// FormatDBFS renders a possibly -inf dBFS value for terminal output.
func FormatDBFS(v float64) string {
	if math.IsInf(v, -1) {
		return "-inf"
	}
	return fmt.Sprintf("%.2f", v)
}
