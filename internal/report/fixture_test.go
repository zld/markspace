package report_test

import (
	"bytes"
	"testing"

	"tsinspect/internal/demux"
	"tsinspect/internal/report"
)

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

func psi(tableID uint8, ext uint16, version uint8, body []byte) []byte {
	length := 5 + len(body) + 4
	s := []byte{tableID, 0xb0 | byte(length>>8), byte(length),
		byte(ext >> 8), byte(ext), 0xc0 | version<<1 | 1, 0, 0}
	s = append(s, body...)
	c := crc32(s)
	return append(s, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

func pat(version uint8, pairs ...[2]uint16) []byte {
	var body []byte
	for _, p := range pairs {
		body = append(body, byte(p[0]>>8), byte(p[0]), 0xe0|byte(p[1]>>8), byte(p[1]))
	}
	return psi(0x00, 1, version, body)
}

func pmt(program uint16, version uint8, pcr uint16, streams ...[2]uint16) []byte {
	body := []byte{0xe0 | byte(pcr>>8), byte(pcr), 0xf0, 0x00}
	for _, es := range streams {
		body = append(body, byte(es[0]), 0xe0|byte(es[1]>>8), byte(es[1]), 0xf0, 0x00)
	}
	return psi(0x02, program, version, body)
}

func pkt(pid uint16, cc uint8, pusi bool, payload []byte) []byte {
	p := make([]byte, 188)
	p[0] = 0x47
	if pusi {
		p[1] = 0x40
	}
	p[1] |= byte(pid >> 8)
	p[2] = byte(pid)
	p[3] = 0x10 | cc&0x0f
	copy(p[4:], payload)
	for i := 4 + len(payload); i < len(p); i++ {
		p[i] = 0xff
	}
	return p
}

func sectionPackets(pid uint16, cc *uint8, section []byte) [][]byte {
	var out [][]byte
	first := true
	for len(section) > 0 {
		chunk := section
		if len(chunk) > 183 {
			chunk = chunk[:183]
		}
		var payload []byte
		pusi := len(out) == 0
		if first {
			payload = append([]byte{0}, chunk...)
			first = false
		} else {
			payload = chunk
		}
		out = append(out, pkt(pid, *cc, pusi, payload))
		*cc = (*cc + 1) & 0x0f
		section = section[len(chunk):]
	}
	return out
}

func esPackets(pid uint16, cc *uint8, n int) [][]byte {
	var out [][]byte
	for i := 0; i < n; i++ {
		out = append(out, pkt(pid, *cc, i == 0, []byte{0, 0, 1, 0xe0}))
		*cc = (*cc + 1) & 0x0f
	}
	return out
}

func render(t *testing.T, data []byte, program int) []byte {
	t.Helper()
	p := demux.NewPipeline(bytes.NewReader(data), demux.Config{Program: program, MaxBufferPackets: 32}, nil)
	res, err := p.Run()
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	var slice *demux.SliceStats
	if program > 0 {
		st := res.Slice
		slice = &st
	}
	var buf bytes.Buffer
	if err := report.WriteJSON(&buf, report.Build("fixture.ts", res, slice, nil)); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
