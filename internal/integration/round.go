package integration

import (
	"context"
	"fmt"
	"time"

	"github.com/pion/webrtc/v4"

	relaypkg "looprelay/internal/relay"
	"looprelay/internal/report"
	rtcpkg "looprelay/internal/rtc"
	rtpsource "looprelay/internal/rtp"
	"looprelay/internal/signal"
)

type hopPair struct {
	ingress rtcpkg.Pair
	egress  rtcpkg.Pair
}

func runRound(ctx context.Context, opts Options, client *signal.Client, requestID int64, started time.Time) (Result, error) {
	policy := rtcpkg.DefaultPolicy()
	policy.ICETimeout = opts.Timeout

	source, err := rtcpkg.NewPeer("source", policy)
	if err != nil {
		return failure(ExitSignaling, err), err
	}
	ingress, err := rtcpkg.NewPeer("forwarder-ingress", policy)
	if err != nil {
		_ = source.Close()
		return failure(ExitSignaling, err), err
	}
	egress, err := rtcpkg.NewPeer("forwarder-egress", policy)
	if err != nil {
		_ = source.Close()
		_ = ingress.Close()
		return failure(ExitSignaling, err), err
	}
	sink, err := rtcpkg.NewPeer("sink", policy)
	if err != nil {
		_ = source.Close()
		_ = ingress.Close()
		_ = egress.Close()
		return failure(ExitSignaling, err), err
	}
	peers := []*rtcpkg.Peer{source, ingress, egress, sink}
	defer func() {
		for _, p := range peers {
			_ = p.Close()
		}
	}()

	upstreamTrack, err := webrtc.NewTrackLocalStaticRTP(vp8Capability(), "video", "looprelay")
	if err != nil {
		return failure(ExitSignaling, err), err
	}
	if _, err := source.PC().AddTrack(upstreamTrack); err != nil {
		return failure(ExitSignaling, fmt.Errorf("source add track: %w", err)), err
	}
	downstreamTrack, err := webrtc.NewTrackLocalStaticRTP(vp8Capability(), "video", "looprelay")
	if err != nil {
		return failure(ExitSignaling, err), err
	}
	if _, err := egress.PC().AddTrack(downstreamTrack); err != nil {
		return failure(ExitSignaling, fmt.Errorf("forwarder add track: %w", err)), err
	}
	if _, err := sink.PC().AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{
		Direction: webrtc.RTPTransceiverDirectionRecvonly,
	}); err != nil {
		return failure(ExitSignaling, fmt.Errorf("sink add transceiver: %w", err)), err
	}

	fwd, err := relaypkg.New(relaypkg.Config{QueueSize: opts.QueueSize}, downstreamTrack)
	if err != nil {
		return failure(ExitSignaling, err), err
	}
	inbound := newInboundForwarder(fwd)
	ingress.PC().OnTrack(inbound.onTrack)

	collector := newCollector()
	sink.PC().OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		collector.attach(t)
	})

	if err := negotiate(ctx, opts, client, requestID, source, ingress, egress, sink); err != nil {
		return failure(classifyExit(err), err), err
	}
	if ctx.Err() != nil {
		return failure(ExitInterrupted, ctx.Err()), ctx.Err()
	}

	for _, p := range peers {
		if err := p.WaitConnected(ctx); err != nil {
			return failure(classifyExit(err), err), err
		}
		if err := p.CheckCandidates(); err != nil {
			return failure(classifyExit(err), err), err
		}
	}
	ingressPair, err := ingress.SelectedPair()
	if err != nil {
		return failure(classifyExit(err), err), err
	}
	egressPair, err := egress.SelectedPair()
	if err != nil {
		return failure(classifyExit(err), err), err
	}

	packets, sched, err := rtpsource.BuildSchedule(rtpsource.Config{
		Packets:    opts.Packets,
		Interval:   opts.Interval,
		DropEvery:  opts.DropEvery,
		DelayEvery: opts.DelayEvery,
		Delay:      opts.Delay,
	})
	if err != nil {
		return failure(ExitInvalidArgs, err), err
	}

	collector.attachCountExpected(packets, sched.StartSeq)

	fwd.Start()
	sendErr := pump(ctx, upstreamTrack, packets)
	if sendErr != nil {
		// Report what actually traversed the pipeline rather than the full
		// plan, so an interrupted run is not dressed up as a partial success.
		rep := buildReport(opts, requestID, hopPair{ingress: ingressPair, egress: egressPair}, sched, collector, fwd.Stats(), started)
		rep.Status = report.StatusFailed
		rep.Error = sendErr.Error()
		return Result{Report: rep, Exit: classifyExit(sendErr), Err: sendErr}, sendErr
	}
	// Let the ingress reader hand every sent packet to the queue before the
	// queue is closed; otherwise the tail is discarded rather than drained.
	inbound.awaitCount(ctx, sched.Generated-sched.Skipped, 2*time.Second)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
	closeErr := fwd.Close(closeCtx)
	closeCancel()

	collector.awaitDrain(sched)
	stats := fwd.Stats()

	rep := buildReport(opts, requestID, hopPair{ingress: ingressPair, egress: egressPair}, sched, collector, stats, started)

	result := Result{Report: rep, Exit: ExitOK}
	if closeErr != nil {
		result.Err = closeErr
		result.Exit = ExitIntegrity
		return result, closeErr
	}
	if !rep.Passed() {
		result.Err = ErrPacketCountMismatch
		result.Exit = ExitIntegrity
	}
	return result, result.Err
}
