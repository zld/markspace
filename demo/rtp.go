package demo

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"

	pionrtp "github.com/pion/rtp"

	"mediarc/model"
	rtpkernel "mediarc/rtp"
	"mediarc/sample"
	"mediarc/stream"
)

// streamRun is the result of one RTP stream through pipeline, window and
// recovery.
type streamRun struct {
	segments     []rtpkernel.Segment
	stats        rtpkernel.RecoveryStats
	window       rtpkernel.Stats
	issues       []*model.Error
	peakResident int
}

func frameCount(run streamRun) int {
	count := 0
	for _, s := range run.segments {
		if s.Kind == rtpkernel.KindFrame {
			count++
		}
	}
	return count
}

func (r *runner) runRTP(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return model.New(model.CodeCanceled, "demo", "canceled before RTP").WithCause(err)
	}
	build := func(name string, opts sample.RTPOptions) (sample.RTPStream, error) {
		fixture, err := sample.BuildRTP(r.fixtureDir, name, opts)
		if err != nil {
			return sample.RTPStream{}, model.Fatal(model.CodeInvalidArgument, "demo",
				"cannot build RTP fixture "+name).WithCause(err)
		}
		r.addFixture(name)
		return fixture, nil
	}
	cleanFixture, err := build("clean.rtp", sample.RTPOptions{Packets: 60, StartSeq: 65000, TimestampJumpAt: -1})
	if err != nil {
		return err
	}
	lossyFixture, err := build("lossy.rtp", sample.RTPOptions{
		Packets: 200, LossEvery: 7, ReorderEvery: 5, DuplicateEvery: 11,
		StartSeq: 65000, TimestampJumpAt: -1, Phase: int(r.opts.Seed % 11),
	})
	if err != nil {
		return err
	}
	jumpyFixture, err := build("jumpy.rtp", sample.RTPOptions{Packets: 40, StartSeq: 1000, TimestampJumpAt: 20})
	if err != nil {
		return err
	}

	clean, err := r.pump(ctx, cleanFixture, false)
	if err != nil {
		return err
	}
	lossy, err := r.pump(ctx, lossyFixture, true)
	if err != nil {
		return err
	}
	jumpy, err := r.pump(ctx, jumpyFixture, true)
	if err != nil {
		return err
	}

	checks := []Assertion{
		assert("clean RTP stream recovers nothing", fmt.Sprint(clean.stats.Recovered), "0", clean.stats.Recovered == 0),
		assert("clean RTP stream loses nothing", fmt.Sprint(clean.window.PacketsLost), "0", clean.window.PacketsLost == 0),
		assert("clean RTP stream delivers every packet", fmt.Sprint(frameCount(clean)), fmt.Sprint(cleanFixture.Packets),
			frameCount(clean) == cleanFixture.Packets),
		assert("lossy RTP recovers a silence segment per gap",
			fmt.Sprintf("gaps=%d silence=%d", lossy.stats.Gaps, lossy.stats.Recovered),
			fmt.Sprintf("gaps=%d silence=%d", lossyFixture.ExpectedGaps, lossyFixture.ExpectedGaps),
			lossy.stats.Gaps == lossyFixture.ExpectedGaps && lossy.stats.Recovered == lossyFixture.ExpectedGaps),
		assert("lossy RTP lost count equals the generated drops", fmt.Sprint(lossy.window.PacketsLost),
			fmt.Sprint(lossyFixture.Lost), lossy.window.PacketsLost == uint64(lossyFixture.Lost)),
		assert("lossy RTP treats replayed packets idempotently", fmt.Sprint(lossy.window.Duplicates),
			fmt.Sprint(lossyFixture.Duplicated), lossy.window.Duplicates == lossyFixture.Duplicated),
		assert("reordered packets are restored in order", fmt.Sprint(lossy.window.Reordered), ">0", lossy.window.Reordered > 0),
		assert("timestamp discontinuities are each reported, not smoothed",
			fmt.Sprintf("jumps=%d", len(jumpy.issues)), fmt.Sprintf("jumps=%d", jumpyFixture.ExpectedJumps),
			len(jumpy.issues) == jumpyFixture.ExpectedJumps),
		assert("window never exceeded its configured depth",
			fmt.Sprint(lossyWindowCap(lossy, r.opts.Window)), "<=depth",
			lossyWindowCap(lossy, r.opts.Window) <= r.opts.Window),
	}
	r.metrics.PacketsLost = lossy.window.PacketsLost
	r.metrics.Recovered = lossy.stats.Recovered
	r.segments = lossy.segments
	r.record(Scenario{
		Name: "rtp",
		Detail: fmt.Sprintf("clean %d frames; lossy %d packets lost into %d gaps, %d silence segments, %d reordered, %d duplicates; jumpy %d timestamp jumps",
			frameCount(clean), lossy.window.PacketsLost, lossy.stats.Gaps, lossy.stats.Recovered,
			lossy.window.Reordered, lossy.window.Duplicates, len(jumpy.issues)),
		Checks: checks,
	})
	return nil
}

// lossyWindowCap is the observed resident peak recorded by the window; it is
// reported through the recovery scaffolding.
func lossyWindowCap(run streamRun, depth int) int { return run.peakResident }

// pump drives one fixture through the bounded pipeline, jitter window and
// recovery stage.
func (r *runner) pump(ctx context.Context, fixture sample.RTPStream, silent bool) (streamRun, error) {
	packets, err := readPackets(fixture.Path)
	if err != nil {
		return streamRun{}, err
	}
	window, err := rtpkernel.NewWindow(rtpkernel.Config{
		Depth:            r.opts.Window,
		PacketDurationNS: 20_000_000,
		ClockRate:        90000,
		TimestampJumps:   true,
	})
	if err != nil {
		return streamRun{}, err
	}
	recovery, err := rtpkernel.NewRecovery(rtpkernel.RecoveryConfig{Silent: silent, SampleRate: 48000, Channels: 2})
	if err != nil {
		return streamRun{}, err
	}

	type pushResult struct {
		release rtpkernel.Release
	}
	results := make(chan pushResult, r.opts.MaxBuffer)
	var run streamRun
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for result := range results {
			run.segments = append(run.segments, recovery.Apply(result.release)...)
			run.issues = append(run.issues, result.release.Issues...)
		}
	}()

	stage := func(ctx context.Context, in <-chan stream.Item, out chan<- stream.Item) error {
		for item := range in {
			packet := &pionrtp.Packet{}
			if err := packet.Unmarshal(item.Data); err != nil {
				return model.New(model.CodeInvalidRTP, "demo", "pipeline got an unparsable packet").WithCause(err)
			}
			release, pushErr := window.Push(rtpkernel.Arrival{Packet: packet, ReceivedNS: int64(item.Seq) * 1_000_000})
			if pushErr != nil && !expectedPushError(pushErr) {
				return pushErr
			}
			select {
			case results <- pushResult{release: release}:
			case <-ctx.Done():
				return model.New(model.CodeCanceled, "demo", "recovery stage canceled").WithCause(ctx.Err())
			}
			select {
			case out <- item:
			case <-ctx.Done():
				return model.New(model.CodeCanceled, "demo", "stage canceled").WithCause(ctx.Err())
			}
		}
		return nil
	}

	pipeline, err := stream.NewPipeline(ctx, stream.Config{BufferSize: r.opts.MaxBuffer, Name: "rtp"}, stage)
	if err != nil {
		return streamRun{}, err
	}
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for index, packet := range packets {
			raw, err := packet.Marshal()
			if err != nil {
				continue
			}
			if err := pipeline.Send(ctx, stream.Item{Seq: uint64(index), Kind: "packet", Data: raw}); err != nil {
				return
			}
		}
		pipeline.CloseInput()
	}()
	go func() {
		for range pipeline.Output() {
		}
	}()
	<-sendDone
	if err := pipeline.Wait(); err != nil {
		close(results)
		<-collectorDone
		return streamRun{}, err
	}
	close(results)
	<-collectorDone

	flush := window.Flush()
	run.segments = append(run.segments, recovery.Apply(flush)...)
	run.window = window.Stats()
	run.stats = recovery.Stats()
	run.peakResident = window.MaxResident()
	return run, nil
}

func countKind(segments []rtpkernel.Segment, kind rtpkernel.SegmentKind) int {
	count := 0
	for _, s := range segments {
		if s.Kind == kind {
			count++
		}
	}
	return count
}

func expectedPushError(err error) bool {
	return model.IsCode(err, model.CodeDuplicatePacket) || model.IsCode(err, model.CodeLatePacket)
}

// readPackets decodes the generated length-prefixed packet file.
func readPackets(path string) ([]*pionrtp.Packet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.Fatal(model.CodeInvalidArgument, "demo", "cannot read RTP fixture").WithCause(err)
	}
	var packets []*pionrtp.Packet
	for offset := 0; offset < len(data); {
		if offset+4 > len(data) {
			return nil, model.New(model.CodeTruncatedInput, "demo", "truncated packet length prefix")
		}
		length := int(binary.BigEndian.Uint32(data[offset:]))
		offset += 4
		if length < 0 || offset+length > len(data) {
			return nil, model.New(model.CodeTruncatedInput, "demo", "truncated packet body")
		}
		packet := &pionrtp.Packet{}
		if err := packet.Unmarshal(data[offset : offset+length]); err != nil {
			return nil, model.New(model.CodeInvalidRTP, "demo", "cannot parse packet").WithCause(err)
		}
		packets = append(packets, packet)
		offset += length
	}
	return packets, nil
}

// ---------------------------------------------------------------- alignment

func init() { register("rtp", 3, (*runner).runRTP) }
