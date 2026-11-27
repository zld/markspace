// Package ts parses MPEG-2 transport streams on strict 188-byte packet
// boundaries.
//
// The reader never fabricates media.  A packet that fails sync, is
// truncated, or whose continuity counter regresses is reported through the
// shared fault model with the exact input offset; its payload is withheld
// from downstream PSI/PES extraction.
package ts

import (
	"example.com/assetpack/internal/fault"
)

// Parse walks data on 188-byte boundaries, resynchronising by a verified
// two-packet sync run when a boundary is damaged.
func Parse(data []byte) *Stream {
	p := &parser{
		data:   data,
		stream: &Stream{Faults: &fault.List{}},
		states: make(map[uint16]*pidState),
	}
	p.stream.Stats.Bytes = len(data)
	p.run()
	return p.stream
}

func (p *parser) run() {
	for off := 0; off+PacketSize <= len(p.data); off += PacketSize {
		if p.data[off] != SyncByte {
			if !p.resync(&off) {
				return
			}
		}
		p.parsePacket(off)
	}
	p.finish()
}

func (p *parser) finish() {
	rem := len(p.data) % PacketSize
	if rem != 0 {
		p.faultTruncated(int64(len(p.data) - rem))
	}
}

func (p *parser) faultTruncated(offset int64) {
	p.stream.Stats.TruncatedBytes = len(p.data) - int(offset)
	p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindTruncated,
		"%d trailing bytes are not a complete 188-byte packet", p.stream.Stats.TruncatedBytes).
		AtOffset(offset).Recover(true))
}

// resync scans forward for two sync bytes exactly one packet apart.  A lone
// 0x47 can occur inside payload, so a single hit is never trusted.
func (p *parser) resync(off *int) bool {
	start := *off
	for cand := start + 1; cand+PacketSize <= len(p.data); cand++ {
		if p.data[cand] != SyncByte {
			continue
		}
		second := cand + PacketSize
		if second >= len(p.data) || p.data[second] == SyncByte {
			p.stream.Stats.SyncErrors++
			p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindSyncByte,
				"lost sync at packet boundary; resumed at next verified sync run").
				AtOffset(int64(cand)).Recover(true))
			*off = cand
			return true
		}
	}
	p.stream.Stats.SyncErrors++
	p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindSyncByte,
		"lost sync and no recoverable packet alignment remains").
		AtOffset(int64(start)).Recover(false))
	return false
}

func (p *parser) parsePacket(off int) {
	raw := p.data[off : off+PacketSize]
	p.stream.Stats.Packets++
	pkt := Packet{
		Offset: int64(off),
		PID:    uint16(raw[1]&0x1F)<<8 | uint16(raw[2]),
		PUSI:   raw[1]&0x40 != 0,
		TEI:    raw[1]&0x80 != 0,
		Scram:  raw[3] >> 6,
		AFC:    (raw[3] >> 4) & 0x3,
		CC:     raw[3] & 0x0F,
	}
	if pkt.AFC == 0 {
		p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindTruncated,
			"reserved adaptation_field_control=0 on PID %d", pkt.PID).
			AtOffset(pkt.Offset).Recover(true))
		return
	}
	payload, ok := p.adapt(&pkt, raw)
	if !ok {
		return
	}
	pkt.Payload = payload
	pkt.Trusted = p.checkCC(&pkt)
	if pkt.HasPCR {
		p.checkPCR(&pkt, pkt.Trusted)
	}
	if !pkt.Trusted || pkt.TEI || pkt.Scram != 0 {
		pkt.Payload = nil
	}
	if pkt.Payload != nil || pkt.Trusted {
		p.stream.Stats.ValidPackets++
	}
	p.stream.Packets = append(p.stream.Packets, pkt)
}

func (p *parser) checkCC(pkt *Packet) bool {
	st := p.state(pkt.PID)
	if !pkt.PayloadPacket() {
		return true
	}
	if !st.haveCC {
		st.haveCC = true
		st.lastCC = pkt.CC
		return true
	}
	expect := (st.lastCC + 1) & 0x0F
	if pkt.CC == st.lastCC {
		p.stream.Stats.CCDuplicates++
		p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindCCDuplicate,
			"duplicate continuity counter %d on PID %d", pkt.CC, pkt.PID).
			AtOffset(pkt.Offset).Recover(true))
		return false
	}
	if pkt.CC != expect {
		p.stream.Stats.CCJumps++
		p.stream.Faults.Add(fault.New(fault.StageParse, fault.KindCCJump,
			"continuity counter jumped %d -> %d on PID %d", st.lastCC, pkt.CC, pkt.PID).
			AtOffset(pkt.Offset).Recover(true))
		st.lastCC = pkt.CC
		return false
	}
	st.lastCC = pkt.CC
	return true
}
