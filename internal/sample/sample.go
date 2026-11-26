package sample

import (
	"fmt"

	"mediapack/internal/clock"
)

// Config controls deterministic fixture generation and anomaly injection.
type Config struct {
	Intervals           int    `json:"intervals"`
	PacketsPerInterval  int    `json:"packets_per_interval"`
	SegDuration         uint64 `json:"seg_duration_90k"`
	WrapPCR             bool   `json:"wrap_pcr"`
	WrapAtInterval      int    `json:"wrap_at_interval"`
	SwitchPSI           bool   `json:"switch_psi"`
	CCJump              bool   `json:"cc_jump"`
	CCDuplicate         bool   `json:"cc_duplicate"`
	SyncError           bool   `json:"sync_error"`
	BadPMTCRC           bool   `json:"bad_pmt_crc"`
	UnresolvedPMTSwitch bool   `json:"unresolved_pmt_switch"`
	TruncatedTail       bool   `json:"truncated_tail"`
	TruncatedTailBytes  int    `json:"truncated_tail_bytes"`
}

// DefaultConfig is the acceptance fixture: four intervals, all anomalies.
func DefaultConfig() Config {
	return Config{
		Intervals:          4,
		PacketsPerInterval: 24,
		SegDuration:        18000, // 2 s per interval at 90 kHz
		WrapPCR:            true,
		WrapAtInterval:     2, // wrap exactly at the boundary before interval 2
		SwitchPSI:          true,
		CCJump:             true,
		CCDuplicate:        true,
		SyncError:          true,
		BadPMTCRC:          true,
		TruncatedTail:      true,
		TruncatedTailBytes: 77,
	}
}

func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.Intervals <= 0 {
		c.Intervals = d.Intervals
	}
	if c.PacketsPerInterval <= 8 {
		c.PacketsPerInterval = d.PacketsPerInterval
	}
	if c.SegDuration == 0 {
		c.SegDuration = d.SegDuration
	}
	if c.TruncatedTailBytes <= 0 {
		c.TruncatedTailBytes = d.TruncatedTailBytes
	}
	if c.WrapAtInterval <= 0 || c.WrapAtInterval >= c.Intervals {
		c.WrapAtInterval = d.WrapAtInterval
	}
	return c
}

// StartTick returns the unwrapped interval start time on the shared timeline.
func (c Config) StartTick(i int) uint64 {
	if !c.WrapPCR {
		return uint64(i) * c.SegDuration
	}
	base := int64(clock.Modulus33) - int64(c.WrapAtInterval)*int64(c.SegDuration)
	return uint64(base + int64(i)*int64(c.SegDuration))
}

// Interval describes one keyframe-aligned range in the generated fixture.
type Interval struct {
	Index          int    `json:"index"`
	StartPacket    int64  `json:"start_packet"`
	EndPacket      int64  `json:"end_packet"`
	KeyframePacket int64  `json:"keyframe_packet"`
	StartOffset    int64  `json:"start_offset"`
	EndOffset      int64  `json:"end_offset"`
	KeyframeOffset int64  `json:"keyframe_offset"`
	StartTick      uint64 `json:"start_tick"`
	EndTick        uint64 `json:"end_tick"`
}

// AnomalyOffsets records exact injected anomaly locations.
type AnomalyOffsets struct {
	CCJumpPacket      int64 `json:"cc_jump_packet"`
	CCJumpOffset      int64 `json:"cc_jump_offset"`
	CCDuplicatePacket int64 `json:"cc_duplicate_packet"`
	CCDuplicateOffset int64 `json:"cc_duplicate_offset"`
	SyncErrorPacket   int64 `json:"sync_error_packet"`
	SyncErrorOffset   int64 `json:"sync_error_offset"`
	BadCRCPacket      int64 `json:"bad_crc_packet"`
	BadCRCOffset      int64 `json:"bad_crc_offset"`
	TruncatedTail     int64 `json:"truncated_tail_offset"`
	WrapBoundary      int64 `json:"wrap_boundary_offset"`
	SwitchInterval    int   `json:"switch_interval"`
	JumpInterval      int   `json:"jump_interval"`
}

// Layout is the generated fixture bytes plus its exact structure.
type Layout struct {
	Config      Config         `json:"config"`
	Intervals   []Interval     `json:"intervals"`
	Anomalies   AnomalyOffsets `json:"anomalies"`
	FullPackets int            `json:"full_packets"`
	Data        []byte         `json:"-"`
}

const (
	switchInterval = 2
	jumpInterval   = 1
)

func keyframePayload(i int) []byte {
	p := []byte{0x00, 0x00, 0x00, 0x01, 0x65} // H.264 IDR NAL
	p = append(p, []byte(fmt.Sprintf("IDR-interval-%02d-", i))...)
	for k := 0; k < 24; k++ {
		p = append(p, byte(0xA0^i^k))
	}
	return p
}

func audioPayload(i int) []byte {
	return []byte{0xFF, 0xF1, byte(0x40 | i), 0x80, 0x1F, 0xFC, 0xDA}
}

func fillerPayload(i, k int) []byte {
	return []byte{0x00, 0x00, 0x01, 0x41, byte(i), byte(k), 0x11, 0x22, 0x33, 0x44}
}

// Generate builds the complete fixture in memory (fixtures are small; the
// production pipeline still streams arbitrary inputs packet by packet).
func Generate(in Config) *Layout {
	cfg := in.withDefaults()
	m := newMuxer()
	L := &Layout{Config: cfg}
	L.Anomalies.SwitchInterval = switchInterval
	L.Anomalies.JumpInterval = jumpInterval

	for i := 0; i < cfg.Intervals; i++ {
		startPkt := len(m.packets)
		startTick := cfg.StartTick(i)

		// 1) keyframe video PES carrying PCR + PTS/DTS.
		if cfg.CCJump && i == jumpInterval {
			m.skipCC(PIDVideo, 2)
			L.Anomalies.CCJumpPacket = int64(startPkt)
			L.Anomalies.CCJumpOffset = int64(startPkt) * 188
		}
		rawTick := startTick % clock.Modulus33
		pes := buildPES(0xE0, &rawTick, &rawTick, keyframePayload(i))
		m.emit(PIDVideo, true, 0x03, pcrAdaptation(rawTick), pes)

		// 2) PAT (version flips at the switch interval).
		patV := byte(0)
		if cfg.SwitchPSI && i >= switchInterval {
			patV = 1
		}
		m.emit(PIDPAT, true, 0x01, nil, psiPacketPayload(buildPAT(patV, PIDPMT)))

		// 3) PMT, with an extra bad-CRC v1 section at the switch.
		if cfg.SwitchPSI && i == switchInterval {
			if cfg.BadPMTCRC {
				badIdx := m.emit(PIDPMT, true, 0x01, nil,
					psiPacketPayload(buildPMT(1, PIDVideo, PIDVideo, PIDAudio)))
				corruptPMTCRC(m.packets[badIdx])
				L.Anomalies.BadCRCPacket = int64(badIdx)
				L.Anomalies.BadCRCOffset = int64(badIdx) * 188
			}
			if !cfg.UnresolvedPMTSwitch {
				m.emit(PIDPMT, true, 0x01, nil,
					psiPacketPayload(buildPMT(1, PIDVideo, PIDVideo, PIDAudio)))
			}
		} else {
			pmtV := byte(0)
			if cfg.SwitchPSI && i > switchInterval {
				pmtV = 1
			}
			m.emit(PIDPMT, true, 0x01, nil,
				psiPacketPayload(buildPMT(pmtV, PIDVideo, PIDVideo, PIDAudio)))
		}

		// 4) audio PES.
		apes := buildPES(0xC0, &rawTick, nil, audioPayload(i))
		m.emit(PIDAudio, true, 0x01, nil, apes)

		// 5) deterministic video fillers up to PacketsPerInterval.
		firstFiller := true
		k := 0
		for len(m.packets) < startPkt+cfg.PacketsPerInterval {
			payload := fillerPayload(i, k)
			if cfg.CCDuplicate && i == 0 && firstFiller {
				idx := m.emitDuplicate(PIDVideo, false, 0x01, nil, payload)
				L.Anomalies.CCDuplicatePacket = int64(idx)
				L.Anomalies.CCDuplicateOffset = int64(idx) * 188
			} else {
				m.emit(PIDVideo, false, 0x01, nil, payload)
			}
			firstFiller = false
			k++
		}

		L.Intervals = append(L.Intervals, Interval{
			Index: i, StartPacket: int64(startPkt), EndPacket: int64(startPkt + cfg.PacketsPerInterval),
			KeyframePacket: int64(startPkt),
			StartOffset:    int64(startPkt) * 188, EndOffset: int64(startPkt+cfg.PacketsPerInterval) * 188,
			KeyframeOffset: int64(startPkt) * 188,
			StartTick:      startTick, EndTick: startTick + cfg.SegDuration,
		})
	}

	// Sync-byte corruption on a filler packet (framing stays intact).
	if cfg.SyncError {
		pkt := cfg.PacketsPerInterval - 1
		if pkt < len(m.packets) {
			m.packets[pkt][0] = 0x46
			L.Anomalies.SyncErrorPacket = int64(pkt)
			L.Anomalies.SyncErrorOffset = int64(pkt) * 188
		}
	}

	// PCR wrap boundary offset = keyframe of WrapAtInterval.
	if cfg.WrapPCR {
		pkt := cfg.WrapAtInterval * cfg.PacketsPerInterval
		L.Anomalies.WrapBoundary = int64(pkt) * 188
	}

	data := make([]byte, 0, len(m.packets)*188+cfg.TruncatedTailBytes)
	for _, p := range m.packets {
		data = append(data, p...)
	}
	L.FullPackets = len(m.packets)

	if cfg.TruncatedTail {
		L.Anomalies.TruncatedTail = int64(len(data))
		tail := make([]byte, cfg.TruncatedTailBytes)
		tail[0] = 0x47
		for k := 1; k < len(tail); k++ {
			tail[k] = byte(0x30 + k%40)
		}
		data = append(data, tail...)
	}
	L.Data = data
	return L
}
