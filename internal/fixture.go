package rtpreplay

import (
	"fmt"
	"math/rand"

	"github.com/pion/rtp"
)

// Case names select a generated scenario. Each case exercises a distinct
// weak-network failure mode and is reproducible from (case, seed).
const (
	CaseNormal        = "normal"
	CaseWrapReorder   = "wrap-reorder"
	CaseDuplicate     = "duplicate"
	CaseBurstLoss     = "burst-loss"
	CaseMalformedOpus = "malformed-opus"
	CaseTimestampJump = "timestamp-jump"
	CaseBlockedDag    = "blocked-dag"
)

// AllCases lists every generated scenario.
var AllCases = []string{
	CaseNormal, CaseWrapReorder, CaseDuplicate, CaseBurstLoss,
	CaseMalformedOpus, CaseTimestampJump, CaseBlockedDag,
}

// Fixture is the generated input bundle for one case.
type Fixture struct {
	Case     string
	Seed     int64
	Trace    *Trace
	MP4Bytes []byte
	MP4Name  string
	Notes    []string
}

// defaultConfig is the configuration digest input for the demo pipeline.
type FixtureConfig struct {
	Streams     int
	OpusRateHz  uint32
	DurationMs  uint64
	EventRateHz uint32
}

// Generate builds the fixture for a case.
func Generate(caseName string, seed int64) (*Fixture, error) {
	switch caseName {
	case CaseNormal:
		return generateNormal(seed)
	case CaseWrapReorder:
		return generateWrapReorder(seed)
	case CaseDuplicate:
		return generateDuplicate(seed)
	case CaseBurstLoss:
		return generateBurstLoss(seed)
	case CaseMalformedOpus:
		return generateMalformedOpus(seed)
	case CaseTimestampJump:
		return generateTimestampJump(seed)
	case CaseBlockedDag:
		return generateBlockedDag(seed)
	default:
		return nil, NewError(CodeInvalidConfig, "generate", fmt.Sprintf("unknown case %q", caseName))
	}
}

// opusFrame20ms returns an Opus packet payload whose TOC encodes a single
// 20 ms frame (config 1) and pads to target bytes with deterministic data.
func opusFrame20ms(rng *rand.Rand, target int) []byte {
	if target < 2 {
		target = 2
	}
	p := make([]byte, target)
	p[0] = 1 << 3 // config 1, code 0, mono
	for i := 1; i < len(p); i++ {
		p[i] = byte(rng.Intn(256))
	}
	return p
}

// opusFrame60ms returns a 60 ms (config 3) single-frame Opus payload.
func opusFrame60ms(rng *rand.Rand, target int) []byte {
	if target < 2 {
		target = 2
	}
	p := make([]byte, target)
	p[0] = 3 << 3
	for i := 1; i < len(p); i++ {
		p[i] = byte(rng.Intn(256))
	}
	return p
}

func rtpHex(seq uint16, ts uint32, marker bool, payload []byte, pt uint8) string {
	p := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: pt, SequenceNumber: seq, Timestamp: ts, Marker: marker}, Payload: payload}
	b, err := p.Marshal()
	if err != nil {
		// rtp.Packet.Marshal only fails on invalid header constants, which the
		// fixtures never set; surface it as a structured config error.
		panic(NewError(CodeInvalidConfig, "fixture_rtp", err.Error()))
	}
	return hexEncode(b)
}

// simpleTrace builds a trace skeleton with the standard clock and events.
func simpleTrace(seed int64, declared uint64) *Trace {
	return &Trace{
		Version: "1",
		Seed:    seed,
		Clock:   TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{
			Track: "opus-main", Codec: CodecOpus, ClockHz: 48000,
			DeclaredDurationMs: declared,
		}},
		Events: standardBandwidthEvents(),
	}
}

// Network model constants for generated fixtures. Media timestamps are the
// sender clock; arrival = media time + base delay + bounded jitter.
const (
	baseDelayMs = 40
	jitterMs    = 8
)

func generateNormal(seed int64) (*Fixture, error) {
	rng := rand.New(rand.NewSource(seed))
	tr := simpleTrace(seed, 1020)
	const n = 51
	var mp4durs []uint32
	var mp4sizes []uint32
	for i := 0; i < n; i++ {
		// Payload size follows the active bandwidth target so a clean run
		// has a meaningful (near-zero) bitrate deviation.
		payload := opusFrame20ms(rng, payloadBytesFor(targetBpsAt(tr.Events, int64(i*20)), 20))
		marker := i == n-1
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    rtpHex(uint16(i+1), uint32(i*960), marker, payload, 111),
			ArrivalMs: int64(baseDelayMs + i*20 + rng.Intn(jitterMs)),
		})
		mp4durs = append(mp4durs, 960)
		mp4sizes = append(mp4sizes, uint32(len(payload)))
	}
	mp4, err := WriteMP4(AudioTrackSpec{Codec: CodecOpus, Timescale: 48000, Durations: mp4durs, Sizes: mp4sizes, OpusPreSkip: 312, OpusRate: 48000, SampleRate: 48000, Channels: 2})
	if err != nil {
		return nil, err
	}
	fx := &Fixture{Case: CaseNormal, Seed: seed, Trace: tr, MP4Bytes: mp4, MP4Name: "opus-normal.mp4"}
	fx.Trace.MP4 = &MP4Ref{FileName: fx.MP4Name, Sha256: sha256Hex(mp4)}
	// Declared duration is n*20ms exactly: a clean run must align at 0ms.
	fx.Trace.Streams[0].DeclaredDurationMs = uint64(n * 20)
	// The normal case also carries an AAC track so both duration sources are
	// exercised end to end in the reference report.
	aacStream, aacDurs, aacSizes := buildAACStream(rng, tr.Events, n, 900)
	tr.Streams = append(tr.Streams, *aacStream)
	multi, err := WriteMP4Tracks([]AudioTrackSpec{
		{Codec: CodecOpus, Timescale: 48000, Durations: mp4durs, Sizes: mp4sizes,
			OpusPreSkip: 312, OpusRate: 48000, SampleRate: 48000, Channels: 2},
		{Codec: CodecAAC, Timescale: 48000, Durations: aacDurs, Sizes: aacSizes,
			AACASC: []byte{0x11, 0x90}, SampleRate: 48000, Channels: 2},
	})
	if err != nil {
		return nil, err
	}
	fx.MP4Bytes = multi
	fx.Trace.MP4 = &MP4Ref{FileName: fx.MP4Name, Sha256: sha256Hex(multi)}
	fx.Notes = append(fx.Notes,
		"51 in-order 20ms Opus packets with two bandwidth changes",
		"51 AAC-LC AUs (1024 samples each) whose AU durations are cross-checked against the MP4 stts table")
	return fx, nil
}
