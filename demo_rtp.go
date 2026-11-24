package mediacore

// rtpStreamPackets is the deterministic source length for the transport
// scenario. It is large enough that the configured loss burst is always
// placed clear of the tail and sits well inside the reorder window.
const rtpStreamPackets = 48

// runRTP generates the disturbed transport stream, recovers it and verifies
// that recovery invented or altered nothing. Bandwidth steps only feed the
// pacing/airtime calculation; the recovered media hash is re-checked after.
func (d *DemoRunner) runRTP(_ *FixturePaths, report *DemoReport) (string, error) {
	packetCount := rtpStreamPackets
	if d.opts.LossBurst > 0 && d.opts.LossBurst+8 > packetCount {
		packetCount = d.opts.LossBurst + 8 // leave packets on both sides of the burst
	}
	cfg := RTPFaultConfig{
		Seed:      d.opts.Seed,
		Packets:   packetCount,
		LossBurst: d.opts.LossBurst,
		Reorder:   d.opts.Reorder,
		Duplicate: d.opts.Duplicate,
		TrackID:   1,
		ClockRate: 90000,
	}
	fix, err := GenerateRTPFaults(cfg)
	if err != nil {
		return "", err
	}
	pipeline := DefaultPipeline()
	st, recovered, sum, err := pipeline.RunRTPPipeline(fix)
	if err != nil {
		return "", err
	}
	d.rtpStats = st
	d.rtpPackets = cfg.Packets

	// Pacing is rhythm-only. The recovered hash is recomputed after the pacing
	// calculation to make explicit that bandwidth never rewrites payload or
	// timestamps.
	delivered, paceSteps, err := ApplyPacing(recovered, d.opts.Bandwidth)
	if err != nil {
		return "", err
	}
	report.PacedPackets = delivered
	if len(d.opts.Bandwidth) > 0 {
		report.BandwidthSteps = len(d.opts.Bandwidth)
	} else {
		report.BandwidthSteps = paceSteps
	}
	if pacedSum := hashRecovered(recovered); pacedSum != sum {
		return "", fail(KindTransport, "demo", "pacing altered recovered payload/timestamp hash")
	}
	for _, kbps := range d.opts.Bandwidth {
		ms, err := PacingAirtimeMillis(recovered, kbps)
		if err != nil {
			return "", err
		}
		d.p("pacing  bandwidth=%dkbps airtime=%dms recovered=%d", kbps, ms, len(recovered))
	}
	d.p("rtp     source=%d wire=%d unique=%d dup=%d reordered=%d missing=%d discarded=%d payload_sha=%s",
		cfg.Packets, fix.Sent, st.Unique, st.Duplicates, st.Reordered, st.Missing, st.Discarded, short(sum))
	return sum, nil
}

// boundedLoss caps the requested burst at the number of packets actually
// available in the fixture stream.
func boundedLoss(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func uniqueOf(st *RecoveryStats) uint64 {
	if st == nil {
		return 0
	}
	return st.Unique
}
