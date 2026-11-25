package kernel

import (
	"encoding/binary"
	"fmt"
)

const (
	TSSyncByte   = 0x47
	TSPacketSize = 188
	MaxTSPackets = 200_000
)

// TSPacket is a parsed fixed-size MPEG-TS packet.
type TSPacket struct {
	PID        uint16
	PUSI       bool
	CC         uint8
	Adaptation uint8
	PCR        *uint64
	Payload    []byte
	Offset     int
}

// TSParser validates TS framing and per-PID continuity counters.
type TSParser struct {
	clock       *Clock
	lastCC      map[uint16]uint8
	haveCC      map[uint16]bool
	lastPCTime  map[uint16]uint64
	lastTime    uint64
	haveTime    bool
	Packets     []TSPacket
	Gaps        []GapEvent
	PacketCount uint64
}

// NewTSParser creates a parser with one shared 90 kHz clock.
func NewTSParser() *TSParser {
	return &TSParser{
		clock:      NewClock(0x0100),
		lastCC:     make(map[uint16]uint8),
		haveCC:     make(map[uint16]bool),
		lastPCTime: make(map[uint16]uint64),
	}
}

// ClockState returns the shared clock checkpoint state.
func (p *TSParser) ClockState() ClockState { return p.clock.State() }

// RestoreClock reloads clock state from a checkpoint.
func (p *TSParser) RestoreClock(st ClockState) { p.clock.RestoreState(st) }

// ParseDatagram parses one RTP payload containing one or more TS packets.
// Datagram boundaries allow recovery from a truncated packet without shifting
// all later 188-byte boundaries.
func (p *TSParser) ParseDatagram(data []byte, sourcePacket uint64) ([]TSPacket, []GapEvent, error) {
	if len(data) > MaxPacketBytes {
		return nil, nil, &MediaError{
			Stage:       "parse",
			Code:        "packet_too_large",
			Message:     fmt.Sprintf("TS datagram length %d exceeds %d", len(data), MaxPacketBytes),
			PID:         0xFFFF,
			Packet:      sourcePacket,
			Recoverable: false,
		}
	}
	var packets []TSPacket
	var gaps []GapEvent
	off := 0
	for len(data)-off >= TSPacketSize {
		if data[off] != TSSyncByte {
			gap := p.syncLossGap(data[off:], off, sourcePacket)
			gaps = append(gaps, gap)
			p.Gaps = append(p.Gaps, gap)
			next := findNextSync(data, off+1)
			if next < 0 {
				return packets, gaps, nil
			}
			off = next
			continue
		}
		pkt, err := p.parsePacket(data[off:off+TSPacketSize], off, sourcePacket)
		if err != nil {
			return packets, gaps, err
		}
		packets = append(packets, pkt)
		p.Packets = append(p.Packets, pkt)
		p.PacketCount++
		gaps = append(gaps, p.checkContinuity(pkt, sourcePacket)...)
		off += TSPacketSize
	}

	if off < len(data) {
		pid := uint16(0xFFFF)
		if len(data)-off >= 3 {
			pid = uint16(data[off+1]&0x1f)<<8 | uint16(data[off+2])
		}
		gap := GapEvent{
			ID:           fmt.Sprintf("ts-trunc-%d", sourcePacket),
			Kind:         GapTruncatedTS,
			PID:          pid,
			Time:         p.currentTimeRange(pid),
			FirstPacket:  sourcePacket,
			LastPacket:   sourcePacket,
			MissingCount: 1,
			Detail:       fmt.Sprintf("short TS payload: %d leftover bytes", len(data)-off),
			Recoverable:  true,
		}
		gaps = append(gaps, gap)
		p.Gaps = append(p.Gaps, gap)
	}
	return packets, gaps, nil
}

func (p *TSParser) parsePacket(raw []byte, offset int, sourcePacket uint64) (TSPacket, error) {
	pid := uint16(raw[1]&0x1f)<<8 | uint16(raw[2])
	pkt := TSPacket{
		PID:        pid,
		PUSI:       raw[1]&0x40 != 0,
		Adaptation: (raw[3] >> 4) & 0x03,
		CC:         raw[3] & 0x0f,
		Offset:     offset,
	}
	payloadOff := 4
	discontinuity := false
	if pkt.Adaptation == 2 || pkt.Adaptation == 3 {
		length := int(raw[4])
		if length > 183 || 5+length > TSPacketSize {
			return pkt, &MediaError{
				Stage:       "parse",
				Code:        "invalid_adaptation_length",
				Message:     fmt.Sprintf("adaptation length %d invalid", length),
				PID:         pid,
				Time:        p.currentTimeRange(pid),
				Packet:      sourcePacket,
				Recoverable: false,
			}
		}
		if length >= 1 {
			discontinuity = raw[5]&0x80 != 0
			if length >= 7 && raw[5]&0x10 != 0 {
				v := uint64(binary.BigEndian.Uint64(append([]byte{0, 0}, raw[6:12]...)))
				base := v >> 15
				unwrapped, err := p.clock.Observe(base, sourcePacket)
				if err != nil {
					return pkt, err
				}
				rel := p.clock.Relative(unwrapped)
				p.lastTime = rel
				p.haveTime = true
				pkt.PCR = &rel
			}
		}
		payloadOff = 5 + length
	}
	if pkt.Adaptation == 1 || pkt.Adaptation == 3 {
		if payloadOff > TSPacketSize {
			return pkt, &MediaError{Stage: "parse", Code: "payload_overflow", PID: pid, Recoverable: false}
		}
		pkt.Payload = append([]byte(nil), raw[payloadOff:]...)
	}
	if discontinuity {
		p.haveCC[pid] = false
	}
	return pkt, nil
}

func (p *TSParser) checkContinuity(pkt TSPacket, sourcePacket uint64) []GapEvent {
	if !p.haveCC[pkt.PID] {
		p.haveCC[pkt.PID] = true
		p.lastCC[pkt.PID] = pkt.CC
		p.lastPCTime[pkt.PID] = p.lastTime
		return nil
	}
	last := p.lastCC[pkt.PID]
	expected := (last + 1) % 16
	if pkt.CC == last {
		// Exact TS duplicate; RTP dedup normally prevents this.
		return nil
	}
	var gaps []GapEvent
	if pkt.CC != expected {
		current := p.lastTime
		previous := p.lastPCTime[pkt.PID]
		g := GapEvent{
			ID:           fmt.Sprintf("ts-cc-%d-%d", pkt.PID, sourcePacket),
			Kind:         GapCCJump,
			PID:          pkt.PID,
			Time:         boundedRange(previous, current),
			FirstPacket:  sourcePacket,
			LastPacket:   sourcePacket,
			MissingCount: int((pkt.CC - expected + 16) % 16),
			Detail:       fmt.Sprintf("CC expected %d got %d after %d", expected, pkt.CC, last),
			Recoverable:  true,
		}
		gaps = append(gaps, g)
		p.Gaps = append(p.Gaps, g)
	}
	p.lastCC[pkt.PID] = pkt.CC
	p.lastPCTime[pkt.PID] = p.lastTime
	return gaps
}

func (p *TSParser) currentTimeRange(pid uint16) TimeRange {
	current := p.lastTime
	if previous, ok := p.lastPCTime[pid]; ok {
		return boundedRange(previous, current)
	}
	return boundedRange(current, current+ClockHz/10)
}

func boundedRange(start, end uint64) TimeRange {
	if end <= start {
		end = start + ClockHz/10
	}
	return TimeRange{Start90k: start, End90k: end}
}

func (p *TSParser) syncLossGap(data []byte, offset int, sourcePacket uint64) GapEvent {
	pid := uint16(0xFFFF)
	if len(data) >= 3 {
		pid = uint16(data[1]&0x1f)<<8 | uint16(data[2])
	}
	return GapEvent{
		ID:          fmt.Sprintf("ts-sync-%d-%d", sourcePacket, offset),
		Kind:        GapSyncLoss,
		PID:         pid,
		Time:        p.currentTimeRange(pid),
		FirstPacket: sourcePacket,
		LastPacket:  sourcePacket,
		Detail:      fmt.Sprintf("missing 0x47 at offset %d", offset),
		Recoverable: true,
	}
}

func findNextSync(data []byte, start int) int {
	for i := start; i+TSPacketSize <= len(data); i++ {
		if data[i] == TSSyncByte {
			return i
		}
	}
	return -1
}
