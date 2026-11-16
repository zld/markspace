// Package simulator generates deterministic RTP test streams and
// serves them over a loopback-only RTSP server.
package simulator

import (
	"encoding/binary"
	"math/rand"
)

// PacketSpec is one RTP packet to send, in send order.
type PacketSpec struct {
	Seq       uint16
	Timestamp uint32
	Marker    bool
	SSRC      uint32
	Payload   []byte
}

const (
	streamSSRC  = 0x11223344
	seqBase     = 65532 // start near the 16 bit wraparound boundary
	tsStep      = 3000  // 90 kHz clock, 30 fps
	frameCount  = 7
	dropFrameIx = 3 // 4th frame loses its FU-A end fragment
)

// sps and pps are deterministic placeholder parameter sets; the audit
// pipeline parses NAL structure only and never decodes video.
var (
	sps = []byte{0x67, 0x42, 0x00, 0x1e, 0x95, 0xa8, 0x28, 0x0f}
	pps = []byte{0x68, 0xce, 0x06, 0xe2}
)

// SPS returns the stream parameter set.
func SPS() []byte { return sps }

// PPS returns the picture parameter set.
func PPS() []byte { return pps }

// Generate builds the send-order packet list for a scenario.
// "stress" exercises reordering, duplication, a missing FU-A fragment
// and sequence wraparound; "normal" is a clean reference stream.
func Generate(scenario string, seed int64) []PacketSpec {
	rng := rand.New(rand.NewSource(seed))
	frames := buildFrames(rng)

	var seq uint16 = seqBase
	type logical struct {
		spec     PacketSpec
		frameIx  int
		fragRole string // "", "fu-start", "fu-mid", "fu-end"
	}
	var all []*logical
	for fi, fr := range frames {
		for pi, pl := range fr.payloads {
			sp := PacketSpec{
				Seq:       seq,
				Timestamp: uint32(fi) * tsStep,
				Marker:    pi == len(fr.payloads)-1,
				SSRC:      streamSSRC,
				Payload:   pl,
			}
			seq++
			all = append(all, &logical{spec: sp, frameIx: fi, fragRole: fr.roles[pi]})
		}
	}

	if scenario == "normal" {
		out := make([]PacketSpec, 0, len(all))
		for _, l := range all {
			out = append(out, l.spec)
		}
		return out
	}

	// stress: drop the FU-A end fragment of frame dropFrameIx,
	// swap the two packets of frame 1, duplicate frame 4's packet.
	var out []PacketSpec
	for _, l := range all {
		if l.frameIx == dropFrameIx && l.fragRole == "fu-end" {
			continue // lost packet: the single sequence gap
		}
		out = append(out, l.spec)
	}
	// swap the two packets belonging to frame index 1
	var pos []int
	for i := range out {
		if out[i].Timestamp == tsStep {
			pos = append(pos, i)
		}
	}
	if len(pos) == 2 {
		out[pos[0]], out[pos[1]] = out[pos[1]], out[pos[0]]
	}
	// duplicate the frame-4 packet (first packet with ts == 4*tsStep)
	for i := range out {
		if out[i].Timestamp == 4*tsStep {
			dup := out[i]
			out = append(out[:i+1], append([]PacketSpec{dup}, out[i+1:]...)...)
			break
		}
	}
	// Padding burst: the gortsplib client buffers out-of-order UDP
	// packets in a 64 slot reorder window with no timeout, so packets
	// following the intentional gap would stay stuck inside the
	// library. Enough consecutive padding packets force the window to
	// overflow and release them. Padding carries a marker payload and
	// is filtered out by the audit client before the RTP pipeline.
	last := out[len(out)-1]
	for i := 0; i < paddingCount; i++ {
		last.Seq++
		out = append(out, PacketSpec{
			Seq:       last.Seq,
			Timestamp: last.Timestamp,
			SSRC:      streamSSRC,
			Payload:   []byte{PaddingMarker},
		})
	}
	return out
}

// PaddingMarker is the single-byte payload marking padding packets.
// NAL type 0 is forbidden in H.264, so it cannot collide with real
// video payloads.
const PaddingMarker = 0x00

// paddingCount exceeds the 64 slot client-side reorder window.
const paddingCount = 70

// IsPaddingPayload reports whether a payload is a padding packet.
func IsPaddingPayload(p []byte) bool {
	return len(p) == 1 && p[0] == PaddingMarker
}

type frame struct {
	payloads [][]byte
	roles    []string
}

func randBytes(rng *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.Intn(256))
	}
	return b
}

func singleNAL(rng *rand.Rand, nalType byte) []byte {
	body := randBytes(rng, 6+rng.Intn(20))
	return append([]byte{0x60 | nalType}, body...)
}

func stapA() []byte {
	out := []byte{0x78} // STAP-A, NRI 3
	var sz [2]byte
	binary.BigEndian.PutUint16(sz[:], uint16(len(sps)))
	out = append(out, sz[:]...)
	out = append(out, sps...)
	binary.BigEndian.PutUint16(sz[:], uint16(len(pps)))
	out = append(out, sz[:]...)
	out = append(out, pps...)
	return out
}

// fuA splits a notional NAL of nalType into n fragments; roles are
// "fu-start", "fu-mid"*, "fu-end".
func fuA(rng *rand.Rand, nalType byte, n int) (payloads [][]byte, roles []string) {
	indicator := byte(0x60 | 28) // keep NRI, type FU-A
	for i := 0; i < n; i++ {
		var hdr byte = nalType
		role := "fu-mid"
		if i == 0 {
			hdr |= 0x80
			role = "fu-start"
		} else if i == n-1 {
			hdr |= 0x40
			role = "fu-end"
		}
		payloads = append(payloads, append([]byte{indicator, hdr}, randBytes(rng, 4+rng.Intn(12))...))
		roles = append(roles, role)
	}
	return payloads, roles
}

// buildFrames creates the 7 frames shared by all scenarios.
func buildFrames(rng *rand.Rand) []frame {
	fu3p, fu3r := fuA(rng, 1, 3)
	fu2p, fu2r := fuA(rng, 1, 2)
	return []frame{
		{payloads: [][]byte{singleNAL(rng, 5)}, roles: []string{""}},                  // 0: IDR single NAL
		{payloads: [][]byte{singleNAL(rng, 1), singleNAL(rng, 1)}, roles: []string{"", ""}}, // 1: two NALs
		{payloads: [][]byte{stapA(), singleNAL(rng, 1)}, roles: []string{"", ""}},     // 2: STAP-A + NAL
		{payloads: fu3p, roles: fu3r},                                                 // 3: FU-A x3 (end lost in stress)
		{payloads: [][]byte{singleNAL(rng, 1)}, roles: []string{""}},                  // 4: duplicated in stress
		{payloads: fu2p, roles: fu2r},                                                 // 5: FU-A x2 complete
		{payloads: [][]byte{singleNAL(rng, 5)}, roles: []string{""}},                  // 6: IDR single NAL
	}
}
