// Package fixture generates deterministic synthetic MPEG-TS streams for the
// demo and integration tests. Nothing references external media: every byte
// is derived from the seed and the requested fault set.
package fixture

import (
	"math/rand"

	"github.com/example/tsrepair/internal/timebase"
	"github.com/example/tsrepair/internal/ts"
)

// Fault names accepted on the command line.
const (
	FaultWrap        = "wrap"
	FaultDuplicate   = "duplicate"
	FaultGap         = "gap"
	FaultPSIVersion  = "psi-version"
	FaultAudioLayout = "audio-layout"
	FaultTruncTail   = "truncated-tail"
)

// AllFaults lists every injectable fault.
var AllFaults = []string{
	FaultWrap, FaultDuplicate, FaultGap, FaultPSIVersion,
	FaultAudioLayout, FaultTruncTail,
}

// Stream is one generated synthetic stream plus the ground truth the demo
// uses to measure round-trip and hit rates.
type Stream struct {
	// Data is the byte stream handed to the parser (may end truncated).
	Data []byte
	// CompleteData excludes the truncated tail so index read-back can be
	// verified against the bytes that really exist.
	CompleteData []byte
	// KeyframeOffsets are byte offsets of every injected keyframe packet.
	KeyframeOffsets []int64
	// TotalPackets is the number of full 188-byte packets before truncation.
	TotalPackets int
	// TruncatedBytes is the length of the dangling tail (0 when clean).
	TruncatedBytes int
	// Faults applied, echoed for the run summary.
	Faults []string
}

// Generator builds streams from a seed and a fault set.
type Generator struct {
	rng        *rand.Rand
	videoPID   uint16
	audioPID   uint16
	pmtPID     uint16
	ccVideo    byte
	ccAudio    byte
	ccPSI      byte
	pcrBase    uint64
	pts        uint64
	pktOrdinal int64
}

// NewGenerator seeds a deterministic generator.
func NewGenerator(seed int64) *Generator {
	return &Generator{
		rng:      rand.New(rand.NewSource(seed)),
		videoPID: 0x0100,
		audioPID: 0x0102,
		pmtPID:   0x1000,
		pcrBase:  90000,
		pts:      0,
	}
}

// faultSet converts the requested names into a lookup map.
func faultSet(names []string) map[string]bool {
	set := map[string]bool{}
	for _, n := range names {
		set[n] = true
	}
	return set
}

// Generate produces a stream of packetsPerSegment*segmentCount-ish packets.
func (g *Generator) Generate(packets int, faults []string) *Stream {
	set := faultSet(faults)
	out := &Stream{Faults: append([]string(nil), faults...)}
	var complete []byte

	emit := func(raw []byte) {
		complete = append(complete, raw...)
		g.pktOrdinal++
	}

	// PAT then PMT, optionally a second PSI version mid-stream.
	emit(g.patPacket(1))
	emit(g.pmtPacket(1))
	psiSwitched := false
	keyframeEvery := 5
	for i := 0; i < packets; i++ {
		if set[FaultPSIVersion] && !psiSwitched && i == packets/2 {
			// New PMT version: keep PIDs so mapping changes are still tracked,
			// but bump the table version and record the boundary.
			emit(g.patPacket(2))
			emit(g.pmtPacket(2))
			psiSwitched = true
		}
		isKey := i%keyframeEvery == 0
		// Video packet with monotonic PCR; inject one 33-bit wrap. The
		// previous packet's base sits near the modulus, this one lands at 40.
		wrapHere := set[FaultWrap] && i == packets/3
		if set[FaultWrap] && i == packets/3-1 {
			g.pcrBase = timebase.Mask33 - 1000
		}
		if wrapHere {
			g.pcrBase = 40
		}
		video := g.videoPacket(isKey, wrapHere)
		emit(video)
		if isKey {
			out.KeyframeOffsets = append(out.KeyframeOffsets,
				int64(len(complete))-int64(ts.PacketSize))
		}
		// Audio packet; layout fault only changes the descriptor, not bytes.
		emit(g.audioPacket(set[FaultAudioLayout]))
		// Duplicate: re-emit the previous video packet verbatim.
		if set[FaultDuplicate] && i == packets/4 {
			emit(video)
		}
		// Gap: advance the video CC without emitting a packet.
		if set[FaultGap] && i == packets/4+1 {
			g.ccVideo = (g.ccVideo + 2) & 0x0F
		}
		g.pcrBase += 3000
		g.pts += 3000
		if g.pcrBase >= timebase.Mask33 {
			g.pcrBase -= timebase.Mask33
		}
	}
	out.TotalPackets = int(g.pktOrdinal)
	out.CompleteData = complete
	out.Data = append(out.Data, complete...)
	if set[FaultTruncTail] {
		// Dangling fragment: sync byte plus a few bytes, no full packet.
		tail := []byte{ts.SyncByte, 0x01, 0x00, 0x30, 0x05, 0xAB, 0xCD}
		out.TruncatedBytes = len(tail)
		out.Data = append(out.Data, tail...)
	}
	return out
}

func (g *Generator) patPacket(version byte) []byte {
	section := buildSection(0x00, version, []byte{
		0x00, 0x01,
		byte(0xE0 | g.pmtPID>>8), byte(g.pmtPID),
	})
	raw := ts.BuildPacket(ts.BuildOpts{
		PID: ts.PIDPAT, PUSI: true, CC: g.nextCC(&g.ccPSI),
		Payload: g.withPointer(section)})
	return raw
}

func (g *Generator) pmtPacket(version byte) []byte {
	body := []byte{
		byte(ts.StreamTypeVideo), byte(0xE0 | g.videoPID>>8), byte(g.videoPID), 0xF0, 0x00,
		byte(ts.StreamTypeAudio), byte(0xE0 | g.audioPID>>8), byte(g.audioPID), 0xF0, 0x00,
	}
	section := buildPMT(version, g.videoPID, body)
	return ts.BuildPacket(ts.BuildOpts{
		PID: g.pmtPID, PUSI: true, CC: g.nextCC(&g.ccPSI),
		Payload: g.withPointer(section)})
}

func (g *Generator) videoPacket(keyframe, _ bool) []byte {
	keyMarker := byte(0)
	if keyframe {
		keyMarker = 1
	}
	return ts.BuildPacket(ts.BuildOpts{
		PID: g.videoPID, PUSI: keyframe,
		AdaptationControl: 3, CC: g.nextCC(&g.ccVideo),
		WithPCR: true, PCRBase: g.pcrBase, PCRExt: uint16(g.rng.Intn(300)),
		RandomAccess: keyframe,
		Payload:      []byte{0x00, 0x00, 0x01, 0xE0, keyMarker, byte(g.pktOrdinal)},
	})
}

func (g *Generator) audioPacket(layoutFault bool) []byte {
	// Marker 0x33 cannot occur in 0xFF-stuffed payloads, so the metadata
	// stage detects the injected layout fault without false positives.
	marker := byte(0xFF)
	if layoutFault {
		marker = 0x33
	}
	return ts.BuildPacket(ts.BuildOpts{
		PID: g.audioPID, PUSI: false, AdaptationControl: 1,
		CC:      g.nextCC(&g.ccAudio),
		Payload: []byte{0xFF, 0xF1, marker, byte(g.pktOrdinal)},
	})
}

func (g *Generator) nextCC(cc *byte) byte {
	v := *cc
	*cc = (*cc + 1) & 0x0F
	return v
}

// withPointer prepends the pointer_field byte and stuffs to 184 bytes.
func (g *Generator) withPointer(section []byte) []byte {
	payload := make([]byte, 184)
	for i := range payload {
		payload[i] = 0xFF
	}
	payload[0] = 0
	copy(payload[1:], section)
	return payload
}

// buildSection assembles a PAT-like section with a dummy CRC.
func buildSection(tableID, version byte, body []byte) []byte {
	secLen := 5 + len(body) + 4 // header after length field + body + crc
	sec := []byte{
		tableID,
		0xB0 | byte(secLen>>8), byte(secLen),
		0x00, 0x01,
		0xC0 | (version << 1) | 0x01,
		0x00, 0x00,
	}
	sec = append(sec, body...)
	return append(sec, 0xDE, 0xAD, 0xBE, 0xEF)
}

// buildPMT assembles a PMT section with PCR PID and elementary stream loop.
func buildPMT(version byte, pcrPID uint16, esLoop []byte) []byte {
	head := []byte{
		0x00, 0x01,
		0xC0 | (version << 1) | 0x01,
		0x00, 0x00,
		byte(0xE0 | pcrPID>>8), byte(pcrPID),
		0xF0, 0x00, // program info length 0
	}
	body := append(head, esLoop...)
	secLen := 5 + len(body) + 4
	sec := []byte{0x02, 0xB0 | byte(secLen>>8), byte(secLen)}
	sec = append(sec, body...)
	return append(sec, 0xCA, 0xFE, 0xBA, 0xBE)
}
