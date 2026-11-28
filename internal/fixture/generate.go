package fixture

import (
	"bytes"
)

// Names of the four generated scenario pairs, without directory or extension.
const (
	NameOK         = "ok"
	NameWrapDrift  = "wrap_drift"
	NamePacketLoss = "packet_loss"
	NameTruncated  = "truncated"
)

// assembleMP4 lays ftyp + mdat + moov out with chunk offsets that point into
// the single shared mdat region, one chunk per track.
func assembleMP4(specs []TrackSpec) []byte {
	ftyp := ftypBox()
	var mdatPayload bytes.Buffer
	chunkOffsets := make([]uint32, len(specs))
	running := uint32(len(ftyp)) + 8 // ftyp size + mdat header
	for i, spec := range specs {
		chunkOffsets[i] = running
		running += trackTotalBytes(spec.SampleSz)
		mdatPayload.Write(make([]byte, trackTotalBytes(spec.SampleSz)))
	}
	mdat := box("mdat", mdatPayload.Bytes())

	payload := mvhdBox(specs[0].Timescale, specs[0].Duration)
	for i, spec := range specs {
		payload = append(payload, trakBox(spec, chunkOffsets[i])...)
	}
	moov := box("moov", payload)

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(mdat)
	out.Write(moov)
	return out.Bytes()
}

// Scenario is one generated input pair.
type Scenario struct {
	Name    string
	MP4     []byte
	Packets []byte
}

// standardTracks builds the two synchronized tracks (dialogue + subtitle)
// sharing a 1kHz time base. Dialogue carries 50ms samples; subtitle marks use
// the same spacing, so their timelines coincide to the sample.
func standardTracks() []TrackSpec {
	return []TrackSpec{
		{
			ID: 1, Timescale: 1000, Duration: MP4SampleCount * 50, Delta: 50,
			Handler: "soun", Name: "dialogue", SampleSz: 16,
		},
		{
			ID: 2, Timescale: 1000, Duration: MP4SampleCount * 50, Delta: 50,
			Handler: "sbtl", Name: "subtitle", SampleSz: 12,
		},
	}
}

// BuildScenarios renders the four deterministic fixture scenarios.
func BuildScenarios() []Scenario {
	okMP4 := assembleMP4(standardTracks())

	// Wrap + drift: packets start near the 32-bit edge so the timestamp wraps
	// cleanly, and each step is 51ms against a 50ms media grid (~2% drift).
	wrapPackets := BuildPackets(PacketOptions{
		Count:          MP4SampleCount,
		StartTimestamp: 1<<32 - 9*51,
		TimestampStep:  51,
		DropSequence:   -1,
	})

	lossPackets := BuildPackets(PacketOptions{
		Count:          MP4SampleCount,
		StartTimestamp: 0,
		TimestampStep:  50,
		DropSequence:   5, // one lost packet, repairable by the XOR frame
		IncludeXOR:     true,
	})

	okPackets := BuildPackets(PacketOptions{
		Count:          MP4SampleCount,
		StartTimestamp: 0,
		TimestampStep:  50,
		DropSequence:   -1,
	})

	// Truncation: a fully valid file cut through its moov box.
	truncated := make([]byte, len(okMP4)-40)
	copy(truncated, okMP4)

	return []Scenario{
		{Name: NameOK, MP4: okMP4, Packets: okPackets},
		{Name: NameWrapDrift, MP4: okMP4, Packets: wrapPackets},
		{Name: NamePacketLoss, MP4: okMP4, Packets: lossPackets},
		{Name: NameTruncated, MP4: truncated, Packets: okPackets},
	}
}
