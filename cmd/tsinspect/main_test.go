package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildFixture writes a small but valid two-program stream to a temp file.
func buildFixture(t *testing.T) string {
	t.Helper()
	var data []byte
	patCC, p1, p2, es1, es2 := uint8(0), uint8(0), uint8(0), uint8(0), uint8(0)
	add := func(ps [][]byte) {
		for _, p := range ps {
			data = append(data, p...)
		}
	}
	add(sectionPkts(0, &patCC, pat(0, [2]uint16{101, 0x1000}, [2]uint16{102, 0x1001})))
	add(sectionPkts(0x1000, &p1, pmt(101, 0, 0x100, [2]uint16{0x1b, 0x100})))
	add(sectionPkts(0x1001, &p2, pmt(102, 0, 0x200, [2]uint16{0x1b, 0x200})))
	add(es(0x100, &es1, 4))
	add(es(0x200, &es2, 4))
	path := filepath.Join(t.TempDir(), "fixture.ts")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func crc(data []byte) uint32 {
	c := uint32(0xffffffff)
	for _, b := range data {
		c ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if c&0x80000000 != 0 {
				c = (c << 1) ^ 0x04c11db7
			} else {
				c <<= 1
			}
		}
	}
	return c
}

func psi(tableID uint8, ext uint16, body []byte) []byte {
	length := 5 + len(body) + 4
	s := []byte{tableID, 0xb0 | byte(length>>8), byte(length), byte(ext >> 8), byte(ext), 0xc1, 0, 0}
	s = append(s, body...)
	c := crc(s)
	return append(s, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

func pat(version uint8, pairs ...[2]uint16) []byte {
	var body []byte
	for _, p := range pairs {
		body = append(body, byte(p[0]>>8), byte(p[0]), 0xe0|byte(p[1]>>8), byte(p[1]))
	}
	return psi(0x00, 1, body)
}

func pmt(program uint16, version uint8, pcr uint16, streams ...[2]uint16) []byte {
	body := []byte{0xe0 | byte(pcr>>8), byte(pcr), 0xf0, 0x00}
	for _, es := range streams {
		body = append(body, byte(es[0]), 0xe0|byte(es[1]>>8), byte(es[1]), 0xf0, 0x00)
	}
	return psi(0x02, program, body)
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

func sectionPkts(pid uint16, cc *uint8, section []byte) [][]byte {
	var out [][]byte
	for len(section) > 0 {
		chunk := section
		if len(chunk) > 183 {
			chunk = chunk[:183]
		}
		start := len(out) == 0
		var payload []byte
		if start {
			payload = append([]byte{0}, chunk...)
		} else {
			payload = chunk
		}
		out = append(out, pkt(pid, *cc, start, payload))
		*cc = (*cc + 1) & 0x0f
		section = section[len(chunk):]
	}
	return out
}

func es(pid uint16, cc *uint8, n int) [][]byte {
	var out [][]byte
	for i := 0; i < n; i++ {
		out = append(out, pkt(pid, *cc, i == 0, []byte{0, 0, 1, 0xe0}))
		*cc = (*cc + 1) & 0x0f
	}
	return out
}

func TestJSONRun(t *testing.T) {
	fixture := buildFixture(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"-json", "-program", "101", fixture}, nil, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	var doc struct {
		PacketCount int64 `json:"packets_processed"`
		Streams     []struct {
			ProgramNumber uint16 `json:"program_number"`
			PID           uint16 `json:"pid"`
			PacketCount   int64  `json:"packet_count"`
		} `json:"elementary_streams"`
		Slice *struct {
			PacketsWritten int64 `json:"packets_written"`
		} `json:"slice"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// 1 PAT + 2 PMT + 8 elementary packets.
	if doc.PacketCount != 11 {
		t.Fatalf("packet count = %d, want 11", doc.PacketCount)
	}
	if len(doc.Streams) != 2 || doc.Streams[0].PacketCount != 4 {
		t.Fatalf("unexpected stream rows: %+v", doc.Streams)
	}
	if doc.Slice == nil || doc.Slice.PacketsWritten == 0 {
		t.Fatalf("slice stats missing without -out: %+v", doc.Slice)
	}
}

func TestMissingInputIsExit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{filepath.Join(t.TempDir(), "nope.ts")}, nil, &stdout, &stderr)
	if code != ExitInput {
		t.Fatalf("exit = %d, want %d", code, ExitInput)
	}
	if !strings.Contains(stderr.String(), "opening input") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestAtomicOutputReplacesOnSuccess(t *testing.T) {
	fixture := buildFixture(t)
	out := filepath.Join(t.TempDir(), "slice.ts")
	if err := os.WriteFile(out, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-program", "101", "-out", out, fixture}, nil, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || len(data)%188 != 0 {
		t.Fatalf("output size %d is not packet aligned", len(data))
	}
	entries, _ := os.ReadDir(filepath.Dir(out))
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %d entries", len(entries))
	}
}

func TestPartialTailExit2KeepsOldOutput(t *testing.T) {
	fixture := buildFixture(t)
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	truncated := filepath.Join(t.TempDir(), "short.ts")
	if err := os.WriteFile(truncated, append(data, 0x47, 0x01), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "slice.ts")
	if err := os.WriteFile(out, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-program", "101", "-out", out, truncated}, nil, &stdout, &stderr); code != ExitInput {
		t.Fatalf("exit = %d, want %d", code, ExitInput)
	}
	if b, _ := os.ReadFile(out); string(b) != "old" {
		t.Fatalf("failed run replaced output: %q", b)
	}
}

func TestBadFlagIsExit2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-program", "x"}, nil, &stdout, &stderr); code != ExitInput {
		t.Fatalf("exit = %d, want %d", code, ExitInput)
	}
}

func TestProgramWithoutOutputIsAllowed(t *testing.T) {
	fixture := buildFixture(t)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-program", "101", fixture}, nil, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "slice program 101") {
		t.Fatalf("slice summary missing: %s", stdout.String())
	}
}
