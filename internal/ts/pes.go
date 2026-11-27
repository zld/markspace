package ts

// PES start code prefix and marker values.
const (
	PESStartCode0 byte = 0x00
	PESStartCode1 byte = 0x00
	PESStartCode2 byte = 0x01
)

// PESPacket is an assembled PES packet spanning one or more TS packets.
type PESPacket struct {
	StreamID   byte
	PTS        int64 // raw 33-bit 90 kHz value, 0 when absent
	HasPTS     bool
	DTS        int64
	HasDTS     bool
	Keyframe   bool // random access indicator from the carrying TS packet
	ByteOffset int64 // offset of the first TS packet of this PES
	Payload    []byte
}

// pesAssembler collects PES bytes for one elementary stream PID. A PES packet
// starts on a PUSI packet and ends when the next PUSI packet for the same PID
// arrives.
type pesAssembler struct {
	pid     uint16
	buf     []byte
	off     int64
	keyframe bool
	active  bool
}

// push feeds one packet and returns a completed PES packet when a new PUSI
// flushes the previous one.
func (a *pesAssembler) push(p *Packet) *PESPacket {
	var completed *PESPacket
	if p.PUSI {
		if a.active && len(a.buf) > 0 {
			completed = parsePES(a.buf, a.off, a.keyframe)
		}
		a.buf = nil
		a.off = p.Offset
		a.keyframe = p.RandomAccess
		a.active = true
	}
	if a.active && len(p.Payload) > 0 {
		a.buf = append(a.buf, p.Payload...)
	}
	return completed
}

// flush returns the final buffered PES packet at end of stream.
func (a *pesAssembler) flush() *PESPacket {
	if !a.active || len(a.buf) == 0 {
		return nil
	}
	pkt := parsePES(a.buf, a.off, a.keyframe)
	a.buf = nil
	a.active = false
	return pkt
}

// parsePES decodes the PES header. Malformed headers yield a packet with
// zeroed timing fields so the caller can count and locate the failure rather
// than panicking.
func parsePES(b []byte, off int64, keyframe bool) *PESPacket {
	pkt := &PESPacket{ByteOffset: off, Keyframe: keyframe}
	// A parseable PES needs start code, stream id, length, the marker/flags
	// pair and the header-data-length byte: 9 bytes minimum.
	if len(b) < 9 || b[0] != 0 || b[1] != 0 || b[2] != 1 {
		return pkt
	}
	pkt.StreamID = b[3]
	ptsDtsFlags := (b[7] >> 6) & 0x03
	hdrLen := int(b[8])
	// Header data bytes occupy b[9 : 9+hdrLen]; validate against the buffer.
	if 9+hdrLen > len(b) {
		return pkt
	}
	switch ptsDtsFlags {
	case 2: // PTS only
		if v, ok := decodeTimeStamp(b[9:14]); ok {
			pkt.PTS, pkt.HasPTS = v, true
		}
	case 3: // PTS + DTS
		if hdrLen < 10 {
			return pkt
		}
		if v, ok := decodeTimeStamp(b[9:14]); ok {
			pkt.PTS, pkt.HasPTS = v, true
		}
		if v, ok := decodeTimeStamp(b[14:19]); ok {
			pkt.DTS, pkt.HasDTS = v, true
		}
	}
	// H.264/H.265 random access: an IDR slice NAL (type 5 for AVC, 19/20 for
	// HEVC) may follow the AUD; treat its presence as a secondary keyframe
	// signal alongside the TS random_access_indicator.
	payloadStart := 9 + hdrLen
	if hasIDRNAL(b[payloadStart:]) {
		pkt.Keyframe = true
	}
	pkt.Payload = b[payloadStart:]
	return pkt
}

// decodeTimeStamp reads the 5-byte MPEG PTS/DTS bit layout.
func decodeTimeStamp(b []byte) (int64, bool) {
	if len(b) < 5 || b[0]&0x01 != 0x01 || b[2]&0x01 != 0x01 || b[4]&0x01 != 0x01 {
		return 0, false
	}
	v := int64(b[0]&0x0E)<<29 |
		int64(b[1])<<22 |
		int64(b[2]&0xFE)<<14 |
		int64(b[3])<<7 |
		int64(b[4]>>1)
	return v, true
}

// hasIDRNAL scans an Annex-B byte stream for an IDR slice NAL unit. The check
// is intentionally small and allocation-free: only NAL headers right after a
// start code are inspected.
func hasIDRNAL(payload []byte) bool {
	for i := 0; i+4 < len(payload); i++ {
		if payload[i] != 0 || payload[i+1] != 0 || payload[i+2] != 1 {
			continue
		}
		nal := payload[i+3]
		nalType := nal & 0x1F // H.264 NAL header low 5 bits
		if nalType == 5 {
			return true // IDR slice
		}
		// H.265 packs the type in the high 6 bits of the 2-byte header.
		if i+4 < len(payload) {
			hevcType := nal >> 1
			if hevcType == 19 || hevcType == 20 {
				return true
			}
		}
	}
	return false
}

// PESDemuxer returns completed PES packets for a set of elementary stream PIDs.
type PESDemuxer struct {
	pids map[uint16]*pesAssembler
}

// NewPESDemuxer builds a demuxer for the given elementary stream PIDs.
func NewPESDemuxer(pids ...uint16) *PESDemuxer {
	d := &PESDemuxer{pids: map[uint16]*pesAssembler{}}
	for _, pid := range pids {
		d.pids[pid] = &pesAssembler{pid: pid}
	}
	return d
}

// HandlePacket feeds one packet; it returns a PES packet when one completes.
func (d *PESDemuxer) HandlePacket(p *Packet) *PESPacket {
	a, ok := d.pids[p.PID]
	if !ok {
		return nil
	}
	return a.push(p)
}

// Flush emits any remaining buffered PES packet for pid, or nil.
func (d *PESDemuxer) Flush(pid uint16) *PESPacket {
	if a, ok := d.pids[pid]; ok {
		return a.flush()
	}
	return nil
}
