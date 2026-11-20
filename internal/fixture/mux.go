package fixture

import (
	"fmt"
	"io"
)

const (
	packetSize  = 188
	payloadSize = packetSize - 4
)

// mux writes deterministic 188-byte transport packets and owns per-PID
// continuity counters. Every malformed packet is avoided by construction so
// the fixtures exercise the parser rather than the writer.
type mux struct {
	w       io.Writer
	cc      map[uint16]uint8
	packets int64
	err     error
}

func newMux(w io.Writer) *mux {
	return &mux{w: w, cc: make(map[uint16]uint8)}
}

func (m *mux) fail(format string, args ...any) {
	if m.err == nil {
		m.err = fmt.Errorf("fixture: "+format, args...)
	}
}

// emit writes one packet. af holds a complete adaptation field including its
// length byte. It never changes the continuity counter.
func (m *mux) emit(pid uint16, pusi bool, afc byte, af, payload []byte) {
	if m.err != nil {
		return
	}
	if len(af)+len(payload) > payloadSize {
		m.fail("pid %#x: adaptation %d + payload %d exceeds %d", pid, len(af), len(payload), payloadSize)
		return
	}
	b := make([]byte, 0, packetSize)
	b = append(b, 0x47)
	var b1 byte = byte(pid >> 8)
	if pusi {
		b1 |= 0x40
	}
	b = append(b, b1, byte(pid), afc|(m.cc[pid]&0x0f))
	b = append(b, af...)
	b = append(b, payload...)
	for len(b) < packetSize {
		b = append(b, 0xff)
	}
	if _, err := m.w.Write(b); err != nil {
		m.fail("writing packet: %v", err)
		return
	}
	m.packets++
}

func (m *mux) advance(pid uint16) {
	m.cc[pid] = (m.cc[pid] + 1) & 0x0f
}

// send writes a payload packet and advances the continuity counter.
func (m *mux) send(pid uint16, payload []byte, pusi bool) {
	m.emit(pid, pusi, 0x10, nil, payload)
	m.advance(pid)
}

// sendWithAF writes a payload packet carrying an adaptation field.
func (m *mux) sendWithAF(pid uint16, af, payload []byte, pusi bool) {
	m.emit(pid, pusi, 0x30, af, payload)
	m.advance(pid)
}

// sendAdaptationOnly writes an adaptation-only packet. Its counter must not
// advance, and parsers must not treat its counter as a payload expectation.
func (m *mux) sendAdaptationOnly(pid uint16, pcrBase uint64) {
	af := make([]byte, payloadSize)
	af[0] = byte(payloadSize - 1)
	if pcrBase != 0 {
		af[1] = 0x10
		putPCR(af[2:], pcrBase)
	}
	m.emit(pid, false, 0x20, af, nil)
}

// sendTEI writes a payload packet whose header sets transport_error_indicator.
func (m *mux) sendTEI(pid uint16, payload []byte) {
	if m.err != nil {
		return
	}
	if len(payload) > payloadSize {
		m.fail("pid %#x: payload too large", pid)
		return
	}
	b := make([]byte, 0, packetSize)
	b = append(b, 0x47, 0x80|byte(pid>>8), byte(pid), 0x10|(m.cc[pid]&0x0f))
	b = append(b, payload...)
	for len(b) < packetSize {
		b = append(b, 0xff)
	}
	if _, err := m.w.Write(b); err != nil {
		m.fail("writing packet: %v", err)
		return
	}
	m.packets++
	m.advance(pid)
}

// duplicate repeats the previous continuity counter without consuming a value.
func (m *mux) duplicate(pid uint16, payload []byte) {
	cur := m.cc[pid]
	m.cc[pid] = (cur + 15) & 0x0f
	m.emit(pid, false, 0x10, nil, payload)
	m.cc[pid] = cur
}

// discontinuity writes a packet with the discontinuity indicator while the
// counter jumps, which must be classified as a signalled discontinuity.
func (m *mux) discontinuity(pid uint16, payload []byte) {
	if len(payload) > payloadSize-2 {
		payload = payload[:payloadSize-2]
	}
	m.emit(pid, false, 0x30, []byte{1, 0x80}, payload)
	m.advance(pid)
}

// lose advances the continuity counter without writing, simulating loss.
func (m *mux) lose(pid uint16, n int) {
	m.cc[pid] = uint8((int(m.cc[pid]) + n) & 0x0f)
}

// section splits one complete PSI section across as many packets as needed.
func (m *mux) section(pid uint16, sec []byte) {
	data := make([]byte, 0, len(sec)+1)
	data = append(data, 0x00) // pointer_field
	data = append(data, sec...)
	first := true
	for len(data) > 0 {
		n := len(data)
		if n > payloadSize {
			n = payloadSize
		}
		m.send(pid, data[:n], first)
		data = data[n:]
		first = false
	}
}

// tail appends a deliberately truncated packet fragment.
func (m *mux) tail(n int) {
	if m.err != nil {
		return
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = 0x47
	}
	if _, err := m.w.Write(b); err != nil {
		m.fail("writing trailing fragment: %v", err)
	}
}

func putPCR(dst []byte, base uint64) {
	pcr := (base&0x1ffffffff)<<15 | 0x3f<<9
	for i := 0; i < 6; i++ {
		dst[i] = byte(pcr >> uint(40-8*i))
	}
}
