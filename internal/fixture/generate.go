package fixture

import (
	"fmt"
	"io"

	"tsrebuild/internal/psi"
)

// Well-known scenario names accepted by the CLI.
const (
	ScenarioBasic    = "basic"
	ScenarioRemap    = "remap-loss"
	ScenarioBadCRC   = "bad-crc"
	ScenarioFragment = "fragmented-psi"
)

// PID layout used by every generated stream.
const (
	PIDPAT     = uint16(0)
	PIDPMT     = uint16(100)
	PIDVideoV0 = uint16(256)
	PIDAudio   = uint16(257)
	PIDVideoV1 = uint16(300)
)

// Phase records one contiguous run of packets sharing the same mapping.
type Phase struct {
	Name        string
	StartPacket int64 // first packet of the phase
	EndPacket   int64 // one past the last packet
	PATVersion  uint8
	PMTVersion  uint8
	VideoPID    uint16
	AudioPID    uint16
	// ES counts emitted in this phase.
	VideoPackets int
	AudioPackets int
	// Continuity anomalies injected in this phase (audio PID).
	AudioGaps int
}

// Manifest is the independently computed description of a generated stream.
type Manifest struct {
	Scenario          string
	TransportStreamID uint16
	ProgramNumber     uint16
	TotalPackets      int64
	Phases            []Phase
	// BadCRCPackets lists packet indexes whose PSI content has a broken CRC.
	BadCRCPackets []int64
}

// Options tunes a generated stream.
type Options struct {
	Scenario string
	// PSIFragmentSize forces PSI sections across multiple packets when > 0
	// and below the section size; zero means each section fits one packet.
	PSIFragmentSize int
}

// esPayload builds a recognizable fixed-size dummy ES payload.
func esPayload(tag byte, n byte) []byte {
	b := make([]byte, 20)
	b[0], b[1] = tag, n
	for i := 2; i < len(b); i++ {
		b[i] = 0xAA
	}
	return b
}

// Generate writes the requested scenario to w and returns its manifest.
func Generate(w io.Writer, opts Options) (*Manifest, error) {
	pw := NewPacketWriter(w)
	m := &Manifest{Scenario: opts.Scenario, TransportStreamID: 1, ProgramNumber: 1}

	emit := func(patVersion, pmtVersion uint8, videoPID uint16, cycles int, phaseName string, gap bool) (Phase, error) {
		phase := Phase{
			Name: phaseName, StartPacket: pw.PacketCount(),
			PATVersion: patVersion, PMTVersion: pmtVersion,
			VideoPID: videoPID, AudioPID: PIDAudio,
		}
		patSec := psi.BuildPATSection(m.TransportStreamID, patVersion, 0, 0,
			[]psi.PATProgram{{ProgramNumber: m.ProgramNumber, PMTPID: PIDPMT}}, 0x1fff)
		pmtSec := psi.BuildPMTSection(m.ProgramNumber, videoPID, pmtVersion,
			[]psi.ElementaryStream{
				{PID: videoPID, StreamType: 0x1b, Kind: psi.StreamVideo},
				{PID: PIDAudio, StreamType: 0x0f, Kind: psi.StreamAudio},
			})
		frag := opts.PSIFragmentSize
		for c := 0; c < cycles; c++ {
			if _, err := pw.WritePSI(PIDPAT, patSec, frag); err != nil {
				return phase, err
			}
			if _, err := pw.WritePSI(PIDPMT, pmtSec, frag); err != nil {
				return phase, err
			}
			if _, _, err := pw.WriteES(videoPID, esPayload('v', byte(c))); err != nil {
				return phase, err
			}
			phase.VideoPackets++
			if gap && c == cycles-1 {
				// Skip the next CC value to inject exactly one audio gap.
				pw.SetCC(PIDAudio, (pw.CC(PIDAudio)+2)&0x0f)
				phase.AudioGaps = 1
			}
			if _, _, err := pw.WriteES(PIDAudio, esPayload('a', byte(c))); err != nil {
				return phase, err
			}
			phase.AudioPackets++
		}
		phase.EndPacket = pw.PacketCount()
		return phase, nil
	}

	switch opts.Scenario {
	case ScenarioBasic:
		ph, err := emit(0, 0, PIDVideoV0, 6, "steady", false)
		if err != nil {
			return nil, err
		}
		m.Phases = append(m.Phases, ph)

	case ScenarioRemap:
		ph0, err := emit(0, 0, PIDVideoV0, 5, "pre-remap", false)
		if err != nil {
			return nil, err
		}
		ph1, err := emit(1, 1, PIDVideoV1, 5, "post-remap", true)
		if err != nil {
			return nil, err
		}
		m.Phases = append(m.Phases, ph0, ph1)

	case ScenarioBadCRC:
		ph, err := emit(0, 0, PIDVideoV0, 3, "stable-v0", false)
		if err != nil {
			return nil, err
		}
		// A corrupt v1 update: it must never become the active mapping.
		badPAT := psi.CorruptCRC(psi.BuildPATSection(m.TransportStreamID, 1, 0, 0,
			[]psi.PATProgram{{ProgramNumber: m.ProgramNumber, PMTPID: PIDPMT}}, 0x1fff))
		idx, err := pw.WritePSI(PIDPAT, badPAT, opts.PSIFragmentSize)
		if err != nil {
			return nil, err
		}
		m.BadCRCPackets = append(m.BadCRCPackets, idx...)
		badPMT := psi.CorruptCRC(psi.BuildPMTSection(m.ProgramNumber, PIDVideoV1, 1,
			[]psi.ElementaryStream{{PID: PIDVideoV1, StreamType: 0x1b, Kind: psi.StreamVideo}}))
		idx, err = pw.WritePSI(PIDPMT, badPMT, opts.PSIFragmentSize)
		if err != nil {
			return nil, err
		}
		m.BadCRCPackets = append(m.BadCRCPackets, idx...)
		// Valid v0 traffic continues and must remain attributed.
		more, err := emit(0, 0, PIDVideoV0, 3, "stable-v0-continued", false)
		if err != nil {
			return nil, err
		}
		m.Phases = append(m.Phases, mergePhases(ph, more))

	case ScenarioFragment:
		ph0, err := emitWithFragment(pw, m, 0, 0, PIDVideoV0, 4, "fragmented-v0", 40)
		if err != nil {
			return nil, err
		}
		ph1, err := emitWithFragment(pw, m, 1, 1, PIDVideoV1, 4, "fragmented-v1", 40)
		if err != nil {
			return nil, err
		}
		m.Phases = append(m.Phases, ph0, ph1)

	default:
		return nil, fmt.Errorf("fixture: unknown scenario %q", opts.Scenario)
	}

	m.TotalPackets = pw.PacketCount()
	return m, nil
}

// emitWithFragment is emit with an explicit per-packet PSI payload limit.
func emitWithFragment(pw *PacketWriter, m *Manifest, patV, pmtV uint8, videoPID uint16,
	cycles int, name string, frag int) (Phase, error) {
	phase := Phase{Name: name, StartPacket: pw.PacketCount(),
		PATVersion: patV, PMTVersion: pmtV, VideoPID: videoPID, AudioPID: PIDAudio}
	patSec := psi.BuildPATSection(m.TransportStreamID, patV, 0, 0,
		[]psi.PATProgram{{ProgramNumber: m.ProgramNumber, PMTPID: PIDPMT}}, 0x1fff)
	pmtSec := psi.BuildPMTSection(m.ProgramNumber, videoPID, pmtV,
		[]psi.ElementaryStream{
			{PID: videoPID, StreamType: 0x1b, Kind: psi.StreamVideo},
			{PID: PIDAudio, StreamType: 0x0f, Kind: psi.StreamAudio},
		})
	for c := 0; c < cycles; c++ {
		if _, err := pw.WritePSI(PIDPAT, patSec, frag); err != nil {
			return phase, err
		}
		if _, err := pw.WritePSI(PIDPMT, pmtSec, frag); err != nil {
			return phase, err
		}
		if _, _, err := pw.WriteES(videoPID, esPayload('v', byte(c))); err != nil {
			return phase, err
		}
		if _, _, err := pw.WriteES(PIDAudio, esPayload('a', byte(c))); err != nil {
			return phase, err
		}
		phase.VideoPackets++
		phase.AudioPackets++
	}
	phase.EndPacket = pw.PacketCount()
	return phase, nil
}

// mergePhases combines adjacent phases with identical bounds bookkeeping.
func mergePhases(a, b Phase) Phase {
	return Phase{
		Name: a.Name, StartPacket: a.StartPacket, EndPacket: b.EndPacket,
		PATVersion: a.PATVersion, PMTVersion: a.PMTVersion,
		VideoPID: a.VideoPID, AudioPID: a.AudioPID,
		VideoPackets: a.VideoPackets + b.VideoPackets,
		AudioPackets: a.AudioPackets + b.AudioPackets,
	}
}
