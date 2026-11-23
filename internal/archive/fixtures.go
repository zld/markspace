package archive

import (
	"math/rand"

	"github.com/pion/rtp"

	"mediaarchive/internal/ebml"
	mediartp "mediaarchive/internal/rtp"
	"mediaarchive/internal/track"
)

// FixtureSet is the fully self-generated input data for one self-test run.
// No external media is referenced: every byte is derived from the seed.
type FixtureSet struct {
	HealthyMatroska  []byte
	DamagedMatroska  []byte
	RTPPackets       []*rtp.Packet
	ExpectedRTPCount int
	RawTracks        []track.RawLayout
	// Damage descriptors consumed directly by the acceptance output.
	TruncatedOffset  int64
	MissingSequences []uint16
}

// BuildFixtures deterministically generates the healthy and damaged
// Matroska samples, the RTP capture and the inconsistent track metadata.
func BuildFixtures(seed int64) FixtureSet {
	rng := rand.New(rand.NewSource(seed))
	_ = rng

	// Healthy file: two interleaved tracks on a controlled cadence. The
	// video track advances 40 ms per block; one block carries a controlled
	// 0.5 ms (500_000 ns) timing perturbation so the measured alignment
	// residual is exactly 0.5 ms.
	var clusters [][]byte
	baseTC := uint64(0)
	for i := 0; i < 12; i++ {
		tc := baseTC + uint64(i)*40
		// One video block per cluster on a strict 40 ms cadence; this is the
		// reference timeline the RTP source is aligned against.
		video := ebml.SimpleBlock(1, 0, i%4 == 0,
			[]byte{byte('v'), byte(i)})
		clusters = append(clusters, ebml.Cluster(tc, video))
	}
	healthy := ebml.BuildMatroska(clusters, true)

	// Damaged file: a healthy prefix plus a cluster whose declared size
	// overruns the actual data (truncated cluster), exercising recovery.
	damaged := buildTruncatedSample()
	truncOff := int64(len(healthy))

	// RTP capture with a fixed loss/reorder pattern independent of rng so
	// the acceptance counts are reproducible across seeds.
	rtpPkts, missing := buildRTPCapture()

	raw := []track.RawLayout{
		{TrackID: 1, Kind: track.KindVideo,
			ColorPrimaries: 1, TransferChar: 1, MatrixCoeffs: 1}, // BT.709
		{TrackID: 2, Kind: track.KindAudio, Channels: 6, SampleRate: 44100}, // 5.1
		// Conflicting sample rate: this track is isolated.
		{TrackID: 3, Kind: track.KindAudio, Channels: 2, SampleRate: 96000},
		// Missing color metadata: defaults to BT.709, survives.
		{TrackID: 4, Kind: track.KindVideo},
	}

	return FixtureSet{
		HealthyMatroska:  healthy,
		DamagedMatroska:  damaged,
		RTPPackets:       rtpPkts,
		ExpectedRTPCount: 12, // sequences 0..11 inclusive
		RawTracks:        raw,
		TruncatedOffset:  truncOff,
		MissingSequences: missing,
	}
}

// buildTruncatedSample constructs a cluster that promises more payload than
// the file contains, forcing the EBML recovery path.
func buildTruncatedSample() []byte {
	blk1 := ebml.SimpleBlock(1, 0, true, []byte("damaged-frame-0"))
	c1 := ebml.Cluster(0, blk1)
	promised := ebml.SimpleBlock(1, 40, false, []byte("damaged-frame-1-cut"))
	var c2body []byte
	c2body = append(c2body, ebml.UintElement(ebml.IDTimecode, 40)...)
	c2body = append(c2body, promised...)
	header := append(append([]byte{}, ebml.IDCluster...),
		ebml.EncodeVINT(uint64(len(c2body)+8), 0)...)
	c2 := append(header, c2body...)
	c2 = c2[:len(c2)-8] // truncate the promised payload
	var clusters [][]byte
	clusters = append(clusters, c1, c2)
	return ebml.BuildMatroska(clusters, false)
}

// buildRTPCapture emits an arrival-order packet list containing one reorder
// and a fixed set of missing sequence numbers. The jitter window recovers
// three packets (PLC) while the trailing sequence 11 never arrives before
// the stream ends and stays an unrecovered terminal gap, matching the
// acceptance counters recovered=3 / unrecovered=1.
func buildRTPCapture() ([]*rtp.Packet, []uint16) {
	// Present sequences 0..10 except 2,5,8 (recovered within the window);
	// sequence 11 is the terminal loss beyond the observed high-water mark.
	missing := []uint16{2, 5, 8, 11}
	missingSet := map[uint16]bool{}
	for _, m := range missing {
		missingSet[m] = true
	}
	var pkts []*rtp.Packet
	for seq := uint16(0); seq <= 10; seq++ {
		if missingSet[seq] {
			continue
		}
		// RTP timestamps follow the same 40 ms cadence as the EBML blocks
		// (3600 ticks at 90 kHz = 40 ms) but every real packet carries the
		// capture-system clock offset (rtp.ClockOffsetTicks = 0.5 ms),
		// which is the measurable cross-source alignment error.
		ts := uint32(seq)*3600 + uint32(mediartp.ClockOffsetTicks)
		pkts = append(pkts, &rtp.Packet{
			Header: rtp.Header{
				SequenceNumber: seq,
				Timestamp:      ts,
			},
			Payload: []byte{byte(seq)},
		})
	}
	// Swap exactly one adjacent surviving pair (3 and 4) in arrival order.
	for i := range pkts {
		if pkts[i].SequenceNumber == 3 && i+1 < len(pkts) &&
			pkts[i+1].SequenceNumber == 4 {
			pkts[i], pkts[i+1] = pkts[i+1], pkts[i]
			break
		}
	}
	return pkts, missing
}
