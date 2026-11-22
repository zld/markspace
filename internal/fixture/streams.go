package fixture

// This file synthesizes the delivery-side RTP streams. Keeping it separate
// from the program description makes the relationship between faults and wire
// evidence explicit.

// buildStreams synthesizes the delivery-side RTP packets in observation order.
func buildStreams(s *Scenario, opts ScenarioOptions, video, audio TrackSpec, frames int, frameTicks int64, _ int) error {
	str := &streamState{seq: 1000, aseq: 4000}
	if opts.Faults[FaultWrap] {
		// Start 1.5 seconds before the 32-bit modulus so it wraps mid-run.
		str.startTS = uint32((1 << 32) - (90000 * 3 / 2))
	}
	if err := videoPackets(s, opts, frames, frameTicks, str); err != nil {
		return err
	}
	if err := audioPackets(s, opts, audio.Count, str); err != nil {
		return err
	}
	applyStreamFaults(s, opts, str)
	return nil
}

// streamState carries the sequence and timestamp cursors across the video and
// audio builders so injected faults stay deterministic.
type streamState struct {
	seq          uint16
	startTS      uint32
	missingTicks uint32
	aseq         uint16
}

// videoPackets emits two fragments per access unit. A dropped tail fragment or
// a whole dropped access unit models delivery loss.
func videoPackets(s *Scenario, opts ScenarioOptions, frames int, frameTicks int64, str *streamState) error {
	for i := 0; i < frames; i++ {
		ts := str.startTS + uint32(int64(i)*frameTicks)
		au := VideoAU(i, opts.Faults[FaultColorConflict], true)
		half := len(au) / 2
		frags := []PacketSpec{
			{Seq: str.seq, TS: ts, Marker: false, Payload: au[:half]},
			{Seq: str.seq + 1, TS: ts, Marker: true, Payload: au[half:]},
		}
		str.seq += 2
		for _, p := range frags {
			if opts.Faults[FaultLoss] && i%11 == 5 {
				str.seq++
				continue // drop the whole access unit
			}
			if opts.Faults[FaultLoss] && i == 20 && p.Marker {
				str.seq++
				continue // drop only the tail fragment
			}
			raw, err := marshalRTP(p)
			if err != nil {
				return err
			}
			s.VideoRTP = append(s.VideoRTP, raw)
		}
	}
	if opts.Faults[FaultLoss] && len(s.VideoRTP) > 4 {
		// Swap two adjacent packets to exercise reorder handling.
		s.VideoRTP[2], s.VideoRTP[3] = s.VideoRTP[3], s.VideoRTP[2]
	}
	if opts.Faults[FaultBackward] {
		// A timestamp jump of 0.5 s backwards, well past any reordering
		// jitter, arrives mid-stream.
		backTicks := uint32(45000)
		bad, err := marshalRTP(PacketSpec{Seq: str.seq, TS: str.startTS + backTicks, Marker: true, Payload: VideoAU(frames, false, true)})
		if err != nil {
			return err
		}
		insertAt := len(s.VideoRTP) / 2
		s.VideoRTP = append(s.VideoRTP[:insertAt], append([][]byte{bad}, s.VideoRTP[insertAt:]...)...)
		str.seq++
	}
	return nil
}

// audioPackets emits one packet per audio frame. A gap advances both the
// sequence numbers and the timestamp grid; a loss advances only the grid.
func audioPackets(s *Scenario, opts ScenarioOptions, count int, str *streamState) error {
	for i := 0; i < count; i++ {
		ts := uint32(int64(i)*1024) + str.missingTicks
		payload := AudioFrame(i, true)
		if opts.Faults[FaultAudioShort] && i == count-1 {
			payload = payload[:64]
		}
		raw, err := marshalRTP(PacketSpec{Seq: str.aseq, TS: ts, Marker: true, Payload: payload})
		if err != nil {
			return err
		}
		str.aseq++
		if opts.Faults[FaultAudioGap] && i == 8 {
			str.aseq += 6
			str.missingTicks += 6 * 1024
		}
		if opts.Faults[FaultAudioLoss] && i == 12 {
			continue
		}
		s.AudioRTP = append(s.AudioRTP, raw)
	}
	return nil
}

// applyStreamFaults injects packet-level faults after both streams exist so
// the effects are independent of the emission order.
func applyStreamFaults(s *Scenario, opts ScenarioOptions, str *streamState) {
	if opts.Faults[FaultAudioOverlap] && len(s.AudioRTP) > 5 {
		s.AudioRTP = append(s.AudioRTP[:5], append([][]byte{s.AudioRTP[4]}, s.AudioRTP[5:]...)...)
	}
}
