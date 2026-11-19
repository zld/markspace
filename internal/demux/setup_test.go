package demux

import (
	"bytes"
	"io"
	"testing"

	"tsinspect/internal/transport"
)

// --- fixture builders -------------------------------------------------------

func crc32(data []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04c11db7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func psiSection(tableID uint8, ext uint16, version uint8, body []byte) []byte {
	length := 5 + len(body) + 4
	s := []byte{tableID, 0xb0 | byte(length>>8), byte(length),
		byte(ext >> 8), byte(ext), 0xc0 | version<<1 | 1, 0, 0}
	s = append(s, body...)
	c := crc32(s)
	return append(s, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

func patBytes(version uint8, programs ...[2]uint16) []byte {
	var body []byte
	for _, p := range programs {
		body = append(body, byte(p[0]>>8), byte(p[0]), 0xe0|byte(p[1]>>8), byte(p[1]))
	}
	return psiSection(0x00, 1, version, body)
}

type esDef struct {
	pid uint16
	typ uint8
}

func pmtBytes(program uint16, version uint8, pcr uint16, streams []esDef) []byte {
	body := []byte{0xe0 | byte(pcr>>8), byte(pcr), 0xf0, 0x00}
	for _, es := range streams {
		body = append(body, es.typ, 0xe0|byte(es.pid>>8), byte(es.pid), 0xf0, 0x00)
	}
	return psiSection(0x02, program, version, body)
}

func tsPacket(pid uint16, cc uint8, pusi bool, payload []byte) []byte {
	pkt := make([]byte, transport.PacketSize)
	pkt[0] = 0x47
	if pusi {
		pkt[1] = 0x40
	}
	pkt[1] |= byte(pid >> 8)
	pkt[2] = byte(pid)
	pkt[3] = 0x10 | cc&0x0f
	copy(pkt[4:], payload)
	for i := 4 + len(payload); i < len(pkt); i++ {
		pkt[i] = 0xff
	}
	return pkt
}

// psiPackets splits a section into 184-byte payload packets, with a
// pointer_field on the packet that starts the section.
func psiPackets(pid uint16, cc *uint8, section []byte) [][]byte {
	var out [][]byte
	for len(section) > 0 {
		chunk := section
		if len(chunk) > 183 {
			chunk = chunk[:183]
		}
		var payload []byte
		start := len(out) == 0
		if start {
			payload = append([]byte{0}, chunk...)
		} else {
			payload = chunk
		}
		out = append(out, tsPacket(pid, *cc, start, payload))
		*cc = (*cc + 1) & 0x0f
		section = section[len(chunk):]
	}
	return out
}

func esPackets(pid uint16, cc *uint8, n int) [][]byte {
	var out [][]byte
	for i := 0; i < n; i++ {
		out = append(out, tsPacket(pid, *cc, i == 0, []byte{0x00, 0x00, 0x01, 0xe0}))
		*cc = (*cc + 1) & 0x0f
	}
	return out
}

type streamBuilder struct{ buf bytes.Buffer }

func (b *streamBuilder) add(pkts [][]byte) {
	for _, p := range pkts {
		b.buf.Write(p)
	}
}

func (b *streamBuilder) raw(p []byte) { b.buf.Write(p) }

func (b *streamBuilder) bytes() []byte { return b.buf.Bytes() }

// runPipeline inspects data and returns the summary and terminal error.
func runPipeline(t *testing.T, data []byte, program int, out io.Writer, maxBuf int) (*Result, error) {
	t.Helper()
	p := NewPipeline(bytes.NewReader(data), Config{
		Program: program, MaxBufferPackets: maxBuf, SliceToDisk: out != nil,
	}, out)
	return p.Run()
}
