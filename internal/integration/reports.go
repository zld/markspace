package integration

import (
	"fmt"
	"time"

	"looprelay/internal/metrics"
	relaypkg "looprelay/internal/relay"
	"looprelay/internal/report"
	rtpsource "looprelay/internal/rtp"
)

func buildReport(opts Options, requestID int64, pairs hopPair, sched rtpsource.Schedule, c *collector, stats relaypkg.Stats, started time.Time) report.Report {
	seq, ts, jitter, received, readErr := c.snapshot()

	rep := report.Report{
		Status:    report.StatusOK,
		Command:   opts.Command,
		RequestID: requestID,
		Links: []report.Link{
			{Name: "ingress", Connected: true, LocalAddress: pairs.ingress.LocalAddress, LocalPort: pairs.ingress.LocalPort, RemoteAddress: pairs.ingress.RemoteAddress, RemotePort: pairs.ingress.RemotePort},
			{Name: "egress", Connected: true, LocalAddress: pairs.egress.LocalAddress, LocalPort: pairs.egress.LocalPort, RemoteAddress: pairs.egress.RemoteAddress, RemotePort: pairs.egress.RemotePort},
		},
		GeneratedPackets: sched.Generated,
		SkippedPackets:   sched.Skipped,
		SentPackets:      sched.Generated - sched.Skipped,
		ForwardedPackets: stats.Forwarded,
		ReceivedPackets:  received,
		LostPackets:      seq.Lost,
		DuplicatePackets: seq.Duplicates,
		ReorderedPackets: seq.Reordered,
		QueueDrops:       stats.Dropped,
		WriteFailures:    stats.WriteFailed,
		JitterMS:         jitter,
		Sequence:         report.ProjectSequence(seq),
		Timestamps:       report.ProjectTimestamps(ts),
		DurationMS:       time.Since(started).Milliseconds(),
	}
	rep.CandidatePairCount = len(rep.Links)
	rep.Checks = checksFor(rep, stats, readErr)
	return rep
}

func checksFor(rep report.Report, stats relaypkg.Stats, readErr error) []report.Check {
	expected := rep.SentPackets
	checks := []report.Check{
		{Name: "candidate_pairs", OK: rep.CandidatePairCount == 2, Detail: fmt.Sprintf("%d links selected pairs", rep.CandidatePairCount)},
		{Name: "forwarded_packets", OK: rep.ForwardedPackets == uint64(expected), Detail: fmt.Sprintf("%d forwarded, %d expected", rep.ForwardedPackets, expected)},
		{Name: "received_packets", OK: rep.ReceivedPackets == expected, Detail: fmt.Sprintf("%d received, %d expected", rep.ReceivedPackets, expected)},
		{Name: "planned_drops", OK: rep.GeneratedPackets-rep.SkippedPackets == expected, Detail: fmt.Sprintf("%d generated - %d skipped", rep.GeneratedPackets, rep.SkippedPackets)},
		{Name: "no_unexpected_loss", OK: rep.LostPackets == 0, Detail: fmt.Sprintf("%d lost", rep.LostPackets)},
		{Name: "no_duplicates", OK: rep.DuplicatePackets == 0 && rep.ReorderedPackets == 0, Detail: fmt.Sprintf("%d duplicates, %d reordered", rep.DuplicatePackets, rep.ReorderedPackets)},
		{Name: "timestamp_continuity", OK: rep.Timestamps.Regressions == 0, Detail: fmt.Sprintf("%d regressions, %d wraps", rep.Timestamps.Regressions, rep.Timestamps.Wraps)},
		{Name: "queue_health", OK: stats.Dropped == 0 && stats.WriteFailed == 0, Detail: fmt.Sprintf("%d drops, %d write failures", stats.Dropped, stats.WriteFailed)},
		{Name: "jitter_status", OK: rep.JitterMS.Status == metrics.JitterStatusOK || rep.JitterMS.Status == metrics.JitterStatusInsufficient, Detail: rep.JitterMS.Status},
	}
	if readErr != nil {
		checks = append(checks, report.Check{Name: "reader", OK: false, Detail: readErr.Error()})
	}
	return checks
}
