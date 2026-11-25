// Package fixture builds deterministic, local-only test media: a fragmented
// MP4 with a dialogue audio track, an effects audio track and a subtitle
// track, plus an RTP stream whose dialogue payloads map one-to-one onto the
// container samples.
//
// Everything is derived from a seed; no pre-existing media is read. The
// package is intentionally a hand-rolled ISO-BMFF writer so that malformed
// cases (truncated packets, short mdat extents) can be expressed exactly.
package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math/rand"

	"github.com/pion/rtp"
)

// Scenario names one demo case.
type Scenario string

const (
	ScenarioNormal    Scenario = "normal"
	ScenarioDuplicate Scenario = "duplicate"
	ScenarioMissing   Scenario = "missing"
	ScenarioTruncated Scenario = "truncated"
	ScenarioCorrupt   Scenario = "corrupt"
	// ScenarioWrap starts the sequence just below the 16-bit ceiling so the
	// stream crosses the wrap point mid-delivery.
	ScenarioWrap Scenario = "wrap"
)

// AllScenarios is the demo order.
var AllScenarios = []Scenario{
	ScenarioNormal, ScenarioDuplicate, ScenarioMissing, ScenarioTruncated, ScenarioCorrupt, ScenarioWrap,
}

// Asset is one generated input pair and its expected outcome.
type Asset struct {
	Scenario   Scenario
	MP4        []byte
	RTP        []byte
	ExpectPass bool
	// Expect contains the invariant the demo asserts, phrased as a note.
	Expect string
}

// opusPattern is one deterministic Opus payload shape. Payloads carry a TOC
// byte with the given frame count; the container sample uses the same size
// and the same 48 kHz duration.
type opusPattern struct {
	config      byte
	code        byte
	frames      int
	samples48k  uint64
	fillerBytes int
}

var patterns = []opusPattern{
	{config: 19, code: 0, frames: 1, samples48k: 960, fillerBytes: 20},  // 20 ms CELT
	{config: 19, code: 1, frames: 2, samples48k: 1920, fillerBytes: 30}, // 2x20 ms
	{config: 16, code: 0, frames: 1, samples48k: 120, fillerBytes: 8},   // 2.5 ms CELT
	{config: 19, code: 0, frames: 1, samples48k: 960, fillerBytes: 24},  // 20 ms CELT
}

func (p opusPattern) toc() byte {
	return p.config<<3 | p.code
}

func (p opusPattern) payload(rnd *rand.Rand) []byte {
	buf := make([]byte, 0, 1+p.fillerBytes)
	buf = append(buf, p.toc())
	if p.code == 1 || p.code == 2 || p.code == 3 {
		buf = append(buf, 0x00) // frame-count byte for code 3, spare for code 1/2
	}
	for len(buf) < 1+p.fillerBytes {
		buf = append(buf, byte(rnd.Intn(251)))
	}
	return buf
}

type audioSample struct {
	payload []byte
	samples uint64 // 48 kHz ticks
}

type plan struct {
	dialogue [][]audioSample // per fragment
	effects  [][]audioSample
	subtitle [][][]byte // per fragment, text payloads
}

func makePlan(seed int64) plan {
	rnd := rand.New(rand.NewSource(seed))
	var p plan
	for frag := 0; frag < 3; frag++ {
		dialogue := make([]audioSample, 0, 4)
		for i := 0; i < 4; i++ {
			pat := patterns[(frag*4+i)%len(patterns)]
			dialogue = append(dialogue, audioSample{payload: pat.payload(rnd), samples: pat.samples48k})
		}
		effects := make([]audioSample, 0, 3)
		for i := 0; i < 3; i++ {
			pat := patterns[(i+1)%len(patterns)]
			effects = append(effects, audioSample{payload: pat.payload(rnd), samples: pat.samples48k})
		}
		texts := [][]byte{[]byte(fmt.Sprintf("dialogue-%d-%02d", frag, rnd.Intn(90)+10))}
		p.dialogue = append(p.dialogue, dialogue)
		p.effects = append(p.effects, effects)
		p.subtitle = append(p.subtitle, texts)
	}
	return p
}

// dialoguePackets flattens the plan into RTP payloads in decode order.
func (p plan) dialoguePackets() [][]byte {
	var out [][]byte
	for _, frag := range p.dialogue {
		for _, s := range frag {
			out = append(out, s.payload)
		}
	}
	return out
}

// Build returns every demo asset for seed.
func Build(seed int64) []Asset {
	base := makePlan(seed)
	assets := make([]Asset, 0, len(AllScenarios))
	for _, sc := range AllScenarios {
		mp4, _, err := buildMP4(base, sc == ScenarioCorrupt)
		if err != nil {
			panic(err)
		}
		rtp := buildRTP(base, sc, seed)
		a := Asset{Scenario: sc, MP4: mp4, RTP: rtp}
		switch sc {
		case ScenarioNormal:
			a.ExpectPass, a.Expect = true, "zero time and byte deviation"
		case ScenarioDuplicate:
			a.ExpectPass, a.Expect = true, "duplicate adds zero time"
		case ScenarioMissing:
			a.ExpectPass, a.Expect = false, "one missing packet with retained gap"
		case ScenarioTruncated:
			a.ExpectPass, a.Expect = false, "truncated packet reported at a byte offset"
		case ScenarioCorrupt:
			a.ExpectPass, a.Expect = false, "corrupt fragment reported at a byte offset"
		case ScenarioWrap:
			a.ExpectPass, a.Expect = true, "sequence wrap extended without duplicate or gap"
		}
		assets = append(assets, a)
	}
	return assets
}

// ---------------------------------------------------------------- RTP ----

func buildRTP(p plan, sc Scenario, seed int64) []byte {
	payloads := p.dialoguePackets()
	// Sequence numbers are derived from the seed so demos are not identical.
	startSeq := uint16(seed*37 + 10)
	if sc == ScenarioWrap {
		startSeq = 65534 // two packets below the ceiling: the stream wraps
	}
	startTS := uint32(seed * 997)
	type pkt struct {
		seq uint16
		ts  uint32
		pl  []byte
	}
	var packets []pkt
	tsOffset := uint64(startTS)
	for i, pl := range payloads {
		seq := uint16(uint64(startSeq) + uint64(i))
		if sc == ScenarioTruncated && i == 1 {
			pl = pl[:1] // TOC promises two frames but the second byte is gone
		}
		packets = append(packets, pkt{seq: seq, ts: uint32(tsOffset), pl: pl})
		if dec, ok := patternByTOC(pl); ok {
			tsOffset += dec
		}
	}
	switch sc {
	case ScenarioDuplicate:
		dup := packets[2]
		packets = append(packets[:3], append([]pkt{dup}, packets[3:]...)...)
	case ScenarioMissing:
		packets = append(packets[:2], packets[3:]...)
	}
	var buf bytes.Buffer
	var lenBuf [4]byte
	for _, pk := range packets {
		packet := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Padding:        false,
				Marker:         true,
				PayloadType:    111,
				SequenceNumber: pk.seq,
				Timestamp:      pk.ts,
				SSRC:           uint32(seed) + 0x5151,
			},
			Payload: pk.pl,
		}
		raw, err := packet.Marshal()
		if err != nil {
			panic(err)
		}
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(raw)))
		buf.Write(lenBuf[:])
		buf.Write(raw)
	}
	return buf.Bytes()
}

// patternByTOC reports the nominal duration of a payload's TOC.
func patternByTOC(pl []byte) (uint64, bool) {
	if len(pl) == 0 {
		return 0, false
	}
	config := pl[0] >> 3
	code := pl[0] & 0x03
	var base uint64
	switch {
	case config <= 3:
		base = []uint64{480, 960, 1920, 2880}[config]
	case config <= 11:
		base = []uint64{480, 960, 1920, 2880}[(config-4)%4]
	case config <= 13:
		base = []uint64{480, 960}[(config-12)%2]
	case config <= 15:
		base = []uint64{480, 960}[(config-14)%2]
	default:
		base = []uint64{120, 240, 480, 960}[(config-16)%4]
	}
	switch code {
	case 0:
		return base, true
	case 1, 2:
		return base * 2, true
	case 3:
		if len(pl) < 2 {
			return 0, false
		}
		return base * uint64(pl[1]&0x3F), true
	}
	return 0, false
}
