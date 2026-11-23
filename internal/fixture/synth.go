package fixture

import (
	"math/rand"
	"sort"
	"strconv"

	"mediaroute/internal/codec"
	"mediaroute/internal/transport"
)

// H.264/H.265 synthetic NAL header bytes (types see codec package).
const (
	h264SPSHeader = 0x67 // type 7
	h264IDRHeader = 0x65 // type 5
	h264PHeader   = 0x41 // type 1 non-IDR slice
	h265IDRHeader = byte(19 << 1)
	h265PHeader   = byte(1 << 1) // TRAIL_R, type 1
)

// synthFrame builds a deterministic bare-stream frame. Keyframes contain a
// key NAL unit; NAL frames end with a trailing start code so the codec
// parser marks every unit complete.
func synthFrame(c codec.Codec, sample uint32, keyframe, complete bool) []byte {
	payload := synthPayload(c, sample)
	switch c {
	case codec.H264:
		if keyframe {
			return appendNAL(appendNAL(nil, h264SPSHeader, payload),
				h264IDRHeader, payload)
		}
		return appendNAL(nil, h264PHeader, payload)
	case codec.H265:
		hdr := h265PHeader
		if keyframe {
			hdr = h265IDRHeader
		}
		return appendNAL(nil, hdr, payload)
	case codec.AV1:
		return synthAV1Frame(keyframe, payload)
	default:
		// Unknown tags are intentionally not synthesized; callers validate.
		return payload
	}
}

func synthPayload(c codec.Codec, sample uint32) []byte {
	// Deterministic, distinctive content derived from sample number only.
	return []byte{
		byte(sample),
		byte(sample >> 8),
		byte(c[0]),
		0xA5,
		0x5A,
		byte(sample) ^ 0xFF,
	}
}

// appendNAL adds [00 00 00 01][header][payload] and a trailing start code
// so the frame is parseable as complete Annex B units.
func appendNAL(dst []byte, header byte, payload []byte) []byte {
	dst = append(dst, 0, 0, 0, 1, header)
	dst = append(dst, payload...)
	dst = append(dst, 0, 0, 1) // completeness terminator
	return dst
}

// synthAV1Frame builds a length-delimited OBU sequence: frame header OBU
// (keyframe bit in first payload byte) plus a tile group OBU.
func synthAV1Frame(keyframe bool, payload []byte) []byte {
	hdrPayload := []byte{0x00}
	if keyframe {
		hdrPayload[0] = 0x80
	}
	out := appendOBU(nil, codec.OBUFrameHeader, hdrPayload)
	out = appendOBU(out, codec.OBUTileGroup, payload)
	return out
}

func appendOBU(dst []byte, typ byte, payload []byte) []byte {
	dst = append(dst, (typ<<3)|0x02) // has_size=1
	dst = append(dst, byte(len(payload)))
	return append(dst, payload...)
}

// fragment splits a synthesized frame into RTP payloads along codec unit
// boundaries. Every returned chunk is independently parseable, so a frame
// whose other fragment was lost still yields a clean (but non-keyframe)
// prefix rather than a half-unit the parser must reject.
func fragment(c codec.Codec, data []byte) [][]byte {
	if c == codec.AV1 {
		// Exactly two OBUs per synthetic AV1 frame: split at the boundary.
		if split := av1Boundary(data); split > 0 {
			return [][]byte{data[:split], data[split:]}
		}
	}
	// Annex B NAL frames: two NAL units; split at the second start code.
	if split := nalBoundary(data); split > 0 {
		return [][]byte{data[:split], data[split:]}
	}
	mid := len(data) / 2
	return [][]byte{data[:mid], data[mid:]}
}

// av1Boundary returns the byte offset where the second OBU starts, or -1.
func av1Boundary(data []byte) int {
	pos := 0
	for obu := 0; obu < 2; obu++ {
		if pos+2 > len(data) {
			return -1
		}
		size := int(data[pos+1])
		next := pos + 2 + size
		if obu == 0 {
			if next <= 0 || next >= len(data) {
				return -1
			}
			return next
		}
		pos = next
	}
	return -1
}

// nalBoundary returns the offset of the second Annex B start code, or -1.
func nalBoundary(data []byte) int {
	for i := 3; i+3 <= len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 &&
			(data[i+2] == 1 || (i+3 < len(data) && data[i+2] == 0 && data[i+3] == 1)) {
			return i
		}
	}
	return -1
}

// applyBandwidthModel reorders arrival positions to mimic a link whose
// available bandwidth keeps changing. Each packet gets a deterministic delay
// from a saw-tooth congestion wave plus occasional micro-bursts, and packets
// are re-emitted in increasing arrival-time order (stable, so equal times keep
// transmission order). A packet can therefore be overtaken by at most `wave`
// successors, keeping every swap inside a reorder window of wave+1. The
// packet multiset is untouched, so conservation holds.
func applyBandwidthModel(seed int64, fx *Fixture, wave int) {
	if wave <= 0 || wave > CongestionWave || len(fx.Packets) < wave {
		return
	}
	rng := rand.New(rand.NewSource(seed ^ 0x5EED_B00))
	type arrival struct {
		pkt   *transport.Packet
		order int // transmission index
		at    int // effective arrival position
	}
	planned := make([]arrival, len(fx.Packets))
	peak := 0
	for i, p := range fx.Packets {
		// Saw-tooth: 0..wave-1, then back down to 0.
		delay := i % wave
		if delay > wave-delay {
			delay = wave - delay
		}
		// Micro-burst: a small deterministic fraction of packets stall longer.
		if rng.Intn(7) == 0 {
			delay += rng.Intn(wave) + 1
		}
		if delay > wave {
			delay = wave
		}
		if delay > peak {
			peak = delay
		}
		planned[i] = arrival{pkt: p, order: i, at: i + delay}
	}
	sort.SliceStable(planned, func(a, b int) bool { return planned[a].at < planned[b].at })
	out := make([]*transport.Packet, len(planned))
	for i, a := range planned {
		out[i] = a.pkt
	}
	fx.Packets = out
	fx.CongestionPeak = peak
}

// CongestionWave bounds how far the bandwidth model may delay a packet. It
// is kept inside the default reorder window so delayed packets stay
// repairable rather than becoming loss.
const CongestionWave = 6

// injectDuplicates re-sends existing packets immediately after their
// originals. Each SSRC draws from its own seed-derived RNG (keyed by SSRC) so
// the result is independent of map iteration and repeatable across builds.
func injectDuplicates(base int64, fx *Fixture, n int) {
	if n <= 0 || len(fx.Packets) == 0 {
		return
	}
	ssrcs := sortedPacketSSRCs(fx)
	remaining := n
	for ti, ssrc := range ssrcs {
		// Distribute the global duplicate budget round-robin across tracks.
		quota := remaining / (len(ssrcs) - ti)
		if quota == 0 {
			break
		}
		rng := rand.New(rand.NewSource(base*1_000_003 + int64(ssrc)))
		used := map[string]bool{}
		var idxs []int
		for i, p := range fx.Packets {
			if p.SSRC == ssrc {
				idxs = append(idxs, i)
			}
		}
		type target struct {
			idx int
			pkt *transport.Packet
		}
		var targets []target
		for tries := 0; tries < quota*20 && len(targets) < quota; tries++ {
			idx := idxs[rng.Intn(len(idxs))]
			src := fx.Packets[idx]
			key := duplicateKey(src.SSRC, src.Seq)
			if used[key] {
				continue
			}
			used[key] = true
			targets = append(targets, target{idx: idx, pkt: &transport.Packet{
				SSRC: src.SSRC, Seq: src.Seq, Timestamp: src.Timestamp,
				Marker: src.Marker, Payload: append([]byte(nil), src.Payload...),
			}})
		}
		remaining -= len(targets)
		sort.Slice(targets, func(i, j int) bool { return targets[i].idx > targets[j].idx })
		for _, tg := range targets {
			fx.Packets = insertAt(fx.Packets, tg.idx+1, tg.pkt)
		}
	}
}

func sortedPacketSSRCs(fx *Fixture) []uint32 {
	seen := map[uint32]bool{}
	var out []uint32
	for _, p := range fx.Packets {
		if !seen[p.SSRC] {
			seen[p.SSRC] = true
			out = append(out, p.SSRC)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func duplicateKey(ssrc uint32, seq uint16) string {
	return strconv.FormatUint(uint64(ssrc), 16) + ":" +
		strconv.FormatUint(uint64(seq), 16)
}

// injectReorders performs in-stream adjacent swaps using a per-SSRC derived
// RNG so ordering is deterministic and independent of map iteration. Each
// swap moves a packet by one arrival position, inside any reorder window
// >= 2, and is therefore fully repairable.
func injectReorders(base int64, fx *Fixture, n int) {
	if n <= 0 || len(fx.Packets) < 4 {
		return
	}
	ssrcs := sortedPacketSSRCs(fx)
	remaining := n
	for ti, ssrc := range ssrcs {
		quota := remaining / (len(ssrcs) - ti)
		if quota == 0 {
			break
		}
		rng := rand.New(rand.NewSource(base*1_000_007 + int64(ssrc)))
		var idxs []int
		for i, p := range fx.Packets {
			if p.SSRC == ssrc {
				idxs = append(idxs, i)
			}
		}
		done := 0
		for tries := 0; tries < quota*20 && done < quota; tries++ {
			if len(idxs) < 2 {
				break
			}
			j := rng.Intn(len(idxs) - 1)
			a, b := idxs[j], idxs[j+1]
			if fx.Packets[a].SSRC != ssrc || fx.Packets[b].SSRC != ssrc ||
				int16(fx.Packets[b].Seq-fx.Packets[a].Seq) != 1 {
				continue
			}
			fx.Packets[a], fx.Packets[b] = fx.Packets[b], fx.Packets[a]
			done++
		}
		remaining -= done
	}
}

func insertAt(pkts []*transport.Packet, at int, p *transport.Packet) []*transport.Packet {
	pkts = append(pkts, nil)
	copy(pkts[at+1:], pkts[at:])
	pkts[at] = p
	return pkts
}
