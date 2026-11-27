// Package fixture: damaged-stream construction for the recovery demo.
package fixture

import (
	"sort"
	"strings"

	"example.com/assetpack/internal/fault"
	"example.com/assetpack/internal/ts"
)

// --- published fault injection ---

// FaultSet names the injected faults requested on the command line.
//
// The same faults always land on the same packet positions (by PID and packet
// ordinal), never by byte arithmetic, so injecting them into an already
// released fixture leaves every healthy byte unchanged.
type FaultSet struct {
	TruncatedTS bool
	CCJump      bool
	PSIUpdate   bool
	CCDuplicate bool
	PCRRegress  bool
	// PSIUpdateOffset is the byte offset at which a new PMT version replaces
	// the original.  Zero means "two thirds of the way through".
	PSIUpdateOffset int
}

// ParseFaultSet parses a comma-separated injection list.
func ParseFaultSet(s string) (FaultSet, error) {
	var fs FaultSet
	if strings.TrimSpace(s) == "" {
		return fs, nil
	}
	for _, part := range strings.Split(s, ",") {
		switch strings.TrimSpace(part) {
		case "":
			continue
		case "truncated-ts":
			fs.TruncatedTS = true
		case "cc-jump":
			fs.CCJump = true
		case "cc-duplicate":
			fs.CCDuplicate = true
		case "psi-update":
			fs.PSIUpdate = true
		case "pcr-regress":
			fs.PCRRegress = true
		default:
			return fs, fault.New(fault.StageCLI, fault.KindInvalidArg,
				"unknown fault %q (known: truncated-ts, cc-jump, cc-duplicate, psi-update, pcr-regress)", part)
		}
	}
	return fs, nil
}

// Names lists the enabled faults in a stable order.
func (fs FaultSet) Names() []string {
	var out []string
	if fs.TruncatedTS {
		out = append(out, "truncated-ts")
	}
	if fs.CCJump {
		out = append(out, "cc-jump")
	}
	if fs.CCDuplicate {
		out = append(out, "cc-duplicate")
	}
	if fs.PSIUpdate {
		out = append(out, "psi-update")
	}
	if fs.PCRRegress {
		out = append(out, "pcr-regress")
	}
	sort.Strings(out)
	return out
}

// Broken builds a damaged stream and reports the offsets each injection
// landed on, so tests and the demo can assert on exact positions rather than
// on vague "somewhere" claims.
type Broken struct {
	Data    []byte
	Offsets map[string]int64
}

// BuildBroken applies the requested faults to a freshly generated stream.
func BuildBroken(opts StreamOptions, fs FaultSet) Broken {
	o := defaultStreamOptions(opts)
	base := BuildTS(o)
	out := append([]byte(nil), base...)
	res := Broken{Data: out, Offsets: map[string]int64{}}

	if fs.CCJump {
		// Corrupt the continuity counter of a mid-stream audio packet and
		// adjust its payload length so parsing stays aligned.
		idx := findNthPacket(out, o.PID, 6)
		if idx >= 0 {
			out[idx+3] = (out[idx+3] & 0xF0) | 0x0F
			res.Offsets["cc-jump"] = int64(idx)
		}
	}
	if fs.CCDuplicate {
		idx := findNthPacket(out, o.PID, 9)
		if prev := findPrevPacket(out, o.PID, idx); idx >= 0 && prev >= 0 {
			// Copy the previous same-PID packet's counter onto this one.
			out[idx+3] = (out[idx+3] & 0xF0) | (out[prev+3] & 0x0F)
			res.Offsets["cc-duplicate"] = int64(idx)
		}
	}
	if fs.PCRRegress {
		idx := findNthPacket(out, o.PCRPID, 8)
		if idx >= 0 && out[idx+5]&ts.AFPCR != 0 {
			// Rewrite the PCR base to a much smaller value without a
			// discontinuity flag: unprovable, so it must be reported.
			for i := 6; i < 11; i++ {
				out[idx+i] = 0x00
			}
			out[idx+10] = 0x00
			out[idx+11] = 0x00
			res.Offsets["pcr-regress"] = int64(idx)
		}
	}
	if fs.PSIUpdate {
		at := fs.PSIUpdateOffset
		if at <= 0 {
			at = len(out) * 2 / 3
		}
		at -= at % ts.PacketSize
		pmt := buildPMT(o.PSI.PMT+1, o.Program, o.PCRPID, o.PID, o.Asset.Channels, o.Asset.Rate)
		packet := packetizePSI(o.PMTPID, 1, pmt)
		// Insert the new PMT version at a packet boundary.
		merged := append([]byte(nil), out[:at]...)
		merged = append(merged, packet...)
		merged = append(merged, out[at:]...)
		out = merged
		res.Data = out
		res.Offsets["psi-update"] = int64(at)
	}
	if fs.TruncatedTS {
		// Cut the stream in the middle of a packet.
		cut := (len(out) / ts.PacketSize) * ts.PacketSize
		cut -= ts.PacketSize / 2
		out = out[:cut]
		res.Data = out
		res.Offsets["truncated-ts"] = int64(cut)
	}
	return res
}

func findPrevPacket(data []byte, pid uint16, before int) int {
	for off := before - ts.PacketSize; off >= 0; off -= ts.PacketSize {
		if data[off] != ts.SyncByte {
			continue
		}
		p := uint16(data[off+1]&0x1F)<<8 | uint16(data[off+2])
		if p == pid {
			return off
		}
	}
	return -1
}

func findNthPacket(data []byte, pid uint16, n int) int {
	count := 0
	for off := 0; off+ts.PacketSize <= len(data); off += ts.PacketSize {
		if data[off] != ts.SyncByte {
			continue
		}
		p := uint16(data[off+1]&0x1F)<<8 | uint16(data[off+2])
		if p != pid {
			continue
		}
		count++
		if count == n {
			return off
		}
	}
	return -1
}
