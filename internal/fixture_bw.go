package rtpreplay

// standardBandwidthEvents is the generated bandwidth trajectory shared by all
// fixture cases: startup, a ramp, then congestion.
func standardBandwidthEvents() []TimedEvent {
	return []TimedEvent{
		{AtMs: 0, Kind: EventBandwidth, ValueBps: 32000, Note: "startup target"},
		{AtMs: 500, Kind: EventBandwidth, ValueBps: 48000, Note: "ramp"},
		{AtMs: 1000, Kind: EventBandwidth, ValueBps: 24000, Note: "congestion"},
	}
}

// targetBpsAt returns the active bandwidth target at a media time.
func targetBpsAt(events []TimedEvent, mediaMs int64) int64 {
	target := int64(0)
	for _, ev := range events {
		if ev.Kind != EventBandwidth {
			continue
		}
		if ev.AtMs <= mediaMs {
			target = ev.ValueBps
		}
	}
	return target
}

// payloadBytesFor returns the payload size in bytes that matches a target
// bitrate over a packet of mediaDurationMs. The generator uses it so a clean
// pass reports ~0% bitrate deviation; it is fixture construction, never a
// parser assumption.
func payloadBytesFor(targetBps int64, mediaDurationMs int64) int {
	if targetBps <= 0 || mediaDurationMs <= 0 {
		return 2
	}
	n := int(targetBps * mediaDurationMs / 8000)
	if n < 2 {
		n = 2
	}
	return n
}

// aacPayloadBytesFor sizes an AAC AU so the resulting per-AU bitrate matches
// targetBps, accounting for the 2-byte AU-headers-length and 2-byte AU header
// that ride in each RTP payload. The interval is in ticks of a 1024-sample AU
// at 48 kHz, so the AU cadence is 21.333ms rather than a whole millisecond.
func aacPayloadBytesFor(targetBps int64) int {
	if targetBps <= 0 {
		return 1
	}
	// bits needed per 1024-sample AU = target * 1024 / 48000.
	bits := targetBps * 1024 / 48000
	n := int(bits/8) - 4
	if n < 1 {
		n = 1
	}
	return n
}
