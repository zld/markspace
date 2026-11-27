package pipeline

import (
	"crypto/sha256"
	"os"
	"strconv"

	"mediatimeline/internal/synthetic"
	"mediatimeline/timeline"
)

// buildTimeline decodes the RTP capture and client events, estimates the
// client clock drift against media anchor points, then drives the watermark.
func buildTimeline(cfg Config, paths *synthetic.FixturePaths, res *Result,
	fail func(Failure)) error {
	// --- RTP: unwrap, dedupe, reorder, report gaps without inventing data ---
	rtpRaw, err := os.ReadFile(paths.RTPFile)
	if err != nil {
		fail(Failure{Code: "RTP_READ_FAILED", Stage: StageRTP, Message: err.Error()})
		return err
	}
	packets, err := synthetic.DecodeRTP(rtpRaw)
	if err != nil {
		fail(Failure{Code: "RTP_DECODE_FAILED", Stage: StageRTP, Message: err.Error()})
		return err
	}
	ssrc := packets[0].SSRC
	stream, err := timeline.NewRTPStream(timeline.RTPConfig{
		SSRC: ssrc, ClockRate: 48000, WindowPackets: 8,
	})
	if err != nil {
		return err
	}
	var rtpItems []timeline.Item
	for _, p := range packets {
		emitted, perr := stream.Push(p)
		if perr != nil {
			fail(Failure{Code: "RTP_BAD_PACKET", Stage: StageRTP, Message: perr.Error()})
			continue
		}
		for _, ep := range emitted {
			extTS := stream.UnwrapTimestamp(ep.Timestamp)
			ns, nerr := stream.TimestampNS(extTS)
			if nerr != nil {
				fail(Failure{Code: "RTP_TS_OVERFLOW", Stage: StageRTP, Message: nerr.Error()})
				continue
			}

			h := sha256.Sum256(ep.Payload)
			rtpItems = append(rtpItems, timeline.Item{
				Kind: timeline.KindRTP, Source: "rtp:audio", NS: ns, EndNS: ns + 20_000_000,
				Sequence: uint64(ep.SequenceNumber), Timestamp: extTS,
				Key: "p" + strconv.FormatUint(uint64(ep.SequenceNumber), 10), Digest: h[:],
			})
		}
	}

	// --- Client events with drift estimation against reference anchors ---
	evRaw, err := os.ReadFile(paths.EventFile)
	if err != nil {
		fail(Failure{Code: "EVENT_READ_FAILED", Stage: StageClock, Message: err.Error()})
		return err
	}
	events, err := synthetic.DecodeEvents(evRaw)
	if err != nil {
		fail(Failure{Code: "EVENT_DECODE_FAILED", Stage: StageClock, Message: err.Error()})
		return err
	}
	anchors := make([]timeline.Anchor, 0, len(events))
	for _, e := range events {
		anchors = append(anchors, timeline.Anchor{RawNS: e.RawNS, ReferenceNS: e.ReferenceNS})
	}
	model, err := timeline.EstimateDrift(anchors)
	if err != nil {
		return err
	}

	wm := timeline.NewWatermark()
	rep := timeline.NewReplayer(wm)
	var maxNS int64
	// On-time events first; late ones are withheld until the watermark passes.
	var lateEvents []synthetic.Event
	for _, e := range events {
		ns, aerr := model.Apply(e.RawNS)
		if aerr != nil {
			fail(Failure{Code: "CLOCK_OVERFLOW", Stage: StageClock, Message: aerr.Error()})
			continue
		}
		if ns > maxNS {
			maxNS = ns
		}
		h := sha256.Sum256([]byte(e.ID))
		it := timeline.Item{Kind: timeline.KindClientEvent, Source: "client",
			NS: ns, RawNS: e.RawNS, Key: e.Name + ":" + e.ID, Digest: h[:]}
		if e.Late {
			lateEvents = append(lateEvents, e)
			continue
		}
		rep.Ingest(it)
	}
	// RTP items extend the watermark beyond the last event.
	for _, it := range rtpItems {
		if it.NS > maxNS {
			maxNS = it.NS
		}
		rep.Ingest(it)
	}
	// Raise the watermark to just before the late item so it triggers replay.
	if len(lateEvents) > 0 {
		rep.Advance(maxNS)
		for _, e := range lateEvents {
			ns, _ := model.Apply(e.RawNS)
			h := sha256.Sum256([]byte(e.ID))
			it := timeline.Item{Kind: timeline.KindClientEvent, Source: "client",
				NS: ns, RawNS: e.RawNS, Key: e.Name + ":" + e.ID, Digest: h[:]}
			if late, from := rep.Ingest(it); late {
				res.RollbackFromNS = from
				res.LateReplayCount++
			}
		}
	}
	rep.Advance(maxNS)

	gaps := stream.Gaps()
	summary := wm.Summary(gaps)
	res.WatermarkNS = summary.WatermarkNS
	res.ReplayDigest = summary.Digest
	res.AlignmentErrorNS = model.MaxAbsResNS
	return nil
}
