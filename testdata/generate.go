// Test-data generator for tsaudit. Run with:
//
//	go run ./testdata/generate.go --scenario normal --out /tmp/sample.ts
//
// Scenarios:
//   normal       120 packets, two complete PMT versions with a PID remap
//   bad-sections incomplete section and a CRC-invalid section, no new version
//   bad-cc       valid tables plus a PID 257 counter jump 5 -> 8
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/example/tsaudit/internal/psi"
	"github.com/example/tsaudit/internal/tsreader"
)

const (
	program1  uint16 = 1
	patPID    uint16 = 0
	pmtPID    uint16 = 4096
	pidVideo0 uint16 = 256
	pidAudio  uint16 = 257
	pidVideo1 uint16 = 258
	pidOther  uint16 = 300
)

func main() {
	out := flag.String("out", "", "output TS file (required)")
	scenario := flag.String("scenario", "normal", "normal|bad-sections|bad-cc")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "generate: --out is required")
		os.Exit(2)
	}

	var packets [][tsreader.PacketSize]byte
	switch *scenario {
	case "normal":
		packets = buildNormal()
	case "bad-sections":
		packets = buildBadSections()
	case "bad-cc":
		packets = buildBadCC()
	default:
		fmt.Fprintf(os.Stderr, "generate: unknown scenario %q\n", *scenario)
		os.Exit(2)
	}

	f, err := os.Create(*out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(2)
	}
	defer f.Close()
	for i := range packets {
		if _, err := f.Write(packets[i][:]); err != nil {
			fmt.Fprintln(os.Stderr, "generate:", err)
			os.Exit(2)
		}
	}
	fmt.Printf("wrote %d packets to %s (scenario=%s)\n", len(packets), *out, *scenario)
}

// psiPackets emits count retransmissions of one section on pid.
func psiPackets(pid uint16, section []byte, count int, startCC uint8) [][tsreader.PacketSize]byte {
	out := make([][tsreader.PacketSize]byte, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, psiPacket(pid, (startCC+uint8(i))&0x0f, section))
	}
	return out
}

// esPackets emits n elementary packets for pid, optionally injecting a CC gap.
func esPackets(pid uint16, n int, tag byte, gapAfter, gapCount int) [][tsreader.PacketSize]byte {
	w := newCCWriter(pid)
	if gapCount > 0 {
		w.withGap(gapAfter, gapCount)
	}
	out := make([][tsreader.PacketSize]byte, 0, n)
	for i := 0; i < n; i++ {
		cc := w.next()
		out = append(out, esPacket(pid, cc, tag))
		w.cc = uint8((int(w.cc) + 1) & 0x0f)
	}
	return out
}

// buildNormal constructs the 120-packet remap fixture described in the
// acceptance criteria.
func buildNormal() [][tsreader.PacketSize]byte {
	pat := buildPAT(0, program1, pmtPID)
	pmtV0 := buildPMT(0, program1, pidVideo0, []psi.StreamInfo{
		{StreamType: stVideo, ElementaryPID: pidVideo0},
		{StreamType: stAudio, ElementaryPID: pidAudio},
		{StreamType: stVideo, ElementaryPID: pidOther},
	})
	pmtV1 := buildPMT(1, program1, pidVideo1, []psi.StreamInfo{
		{StreamType: stVideo, ElementaryPID: pidVideo1},
		{StreamType: stAudio, ElementaryPID: pidAudio},
		{StreamType: stVideo, ElementaryPID: pidOther},
	})

	var out [][tsreader.PacketSize]byte
	// Epoch 0: PAT v0 three times + PMT v0 five times + 12 PID256 + 20 audio +
	// 20 PID300 packets.
	out = append(out, psiPackets(patPID, pat, 3, 0)...)
	out = append(out, psiPackets(pmtPID, pmtV0, 7, 0)...)
	out = append(out, esPackets(pidVideo0, 12, 0x10, -1, 0)...)
	out = append(out, esPackets(pidAudio, 20, 0x20, -1, 0)...)
	out = append(out, esPackets(pidOther, 20, 0x30, -1, 0)...)

	// Epoch 1: PAT v0 retransmitted three times (duplicate) + PMT v1 five
	// times + 8 PID258 + 20 audio + 20 PID300 packets.
	out = append(out, psiPackets(patPID, pat, 3, 3)...)
	out = append(out, psiPackets(pmtPID, pmtV1, 7, 5)...)
	out = append(out, esPackets(pidVideo1, 8, 0x40, -1, 0)...)
	out = append(out, esPackets(pidAudio, 20, 0x21, -1, 0)...)
	out = append(out, esPackets(pidOther, 20, 0x31, -1, 0)...)

	if len(out) != 120 {
		fmt.Fprintf(os.Stderr, "generate: internal error: %d packets, want 120\n", len(out))
		os.Exit(2)
	}
	return out
}

// buildBadCC keeps valid PAT/PMT v0 and jumps PID 257 from counter 5 to 8
// (two missing packets), then continues 9, 10, ...
func buildBadCC() [][tsreader.PacketSize]byte {
	pat := buildPAT(0, program1, pmtPID)
	pmtV0 := buildPMT(0, program1, pidVideo0, []psi.StreamInfo{
		{StreamType: stVideo, ElementaryPID: pidVideo0},
		{StreamType: stAudio, ElementaryPID: pidAudio},
	})
	var out [][tsreader.PacketSize]byte
	out = append(out, psiPackets(patPID, pat, 2, 0)...)
	out = append(out, psiPackets(pmtPID, pmtV0, 2, 0)...)
	out = append(out, esPackets(pidVideo0, 10, 0x10, -1, 0)...)
	// 12 packets, counters 0..4 then 8..13: gap of two missing counters.
	out = append(out, esPackets(pidAudio, 12, 0x20, 5, 2)...)
	return out
}

// buildBadSections publishes one valid PAT/PMT v0, then sends an incomplete
// PMT section and a CRC-corrupted PMT section. Neither damaged attempt may
// publish a new version.
func buildBadSections() [][tsreader.PacketSize]byte {
	pat := buildPAT(0, program1, pmtPID)
	pmtV0 := buildPMT(0, program1, pidVideo0, []psi.StreamInfo{
		{StreamType: stVideo, ElementaryPID: pidVideo0},
		{StreamType: stAudio, ElementaryPID: pidAudio},
	})
	pmtBad := buildPMT(2, program1, pidVideo0, []psi.StreamInfo{
		{StreamType: stVideo, ElementaryPID: pidVideo1},
	})

	var out [][tsreader.PacketSize]byte
	out = append(out, psiPackets(patPID, pat, 2, 0)...)
	out = append(out, psiPackets(pmtPID, pmtV0, 2, 0)...)
	out = append(out, esPackets(pidVideo0, 8, 0x10, -1, 0)...)

	// Incomplete section: PUSI packet carrying only the first bytes of v2,
	// with no continuation packet to complete it.
	incomplete := psiPacket(pmtPID, 2, pmtBad[:12])
	out = append(out, incomplete)

	// CRC-invalid section: flip a payload byte inside a otherwise complete v2.
	corrupt := psiPacket(pmtPID, 3, pmtBad)
	corrupt[20] ^= 0xff
	out = append(out, corrupt)

	out = append(out, esPackets(pidAudio, 8, 0x20, -1, 0)...)
	return out
}
