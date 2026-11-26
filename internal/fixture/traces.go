package fixture

import (
	"math/rand"
	"os"

	"github.com/pion/rtp"

	pwrtp "pwflow/internal/rtp"
)

const fixtureSSRC uint32 = 0x50574632

func writeRTPPair(p *Paths, seed int64) error {
	rng := rand.New(rand.NewSource(seed))
	standard := standardTrace(rng)
	if err := writeTraceFile(p.StandardRTP, standard); err != nil {
		return err
	}
	burst := burstLossTrace(rng)
	return writeTraceFile(p.BurstRTP, burst)
}

func writeTraceFile(path string, records []pwrtp.Captured) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return pwrtp.WriteTrace(f, records)
}

func makeRTP(seqBase int, i int, arrival int64, rng *rand.Rand) pwrtp.Captured {
	payload := make([]byte, 120+(i%16)*8)
	for j := range payload {
		payload[j] = byte(i*31 + j*7 + int(rng.Int63n(1))) // deterministic; rng keeps seed use explicit
	}
	return pwrtp.Captured{
		ArrivalNS: arrival,
		Packet: rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Padding:        false,
				Marker:         i%50 == 49,
				PayloadType:    111,
				SequenceNumber: uint16(seqBase + i),
				Timestamp:      uint32(i * 960),
				SSRC:           fixtureSSRC,
			},
			Payload: payload,
		},
	}
}

func withArrivalJitter(records []pwrtp.Captured, rng *rand.Rand) []pwrtp.Captured {
	var at int64
	incs := []int64{10_000_000, 30_000_000, 20_000_000, 20_000_000}
	for i := range records {
		if i > 0 {
			at += incs[i%len(incs)] + rng.Int63n(2_000_000)
		}
		records[i].ArrivalNS = at
	}
	return records
}

func standardTrace(rng *rand.Rand) []pwrtp.Captured {
	const n = 500
	base := make([]pwrtp.Captured, n)
	for i := 0; i < n; i++ {
		base[i] = makeRTP(65400, i, 0, rng) // crosses 65535 -> 0
	}
	ordered := append([]pwrtp.Captured(nil), base...)
	ordered[20], ordered[21] = ordered[21], ordered[20]
	ordered[200], ordered[201] = ordered[201], ordered[200]
	ordered = withArrivalJitter(ordered, rng)

	// Three exact duplicate transmissions. They remain duplicate records;
	// repair deduplicates media output but manifest reports the count.
	at := ordered[len(ordered)-1].ArrivalNS + 5_000_000
	for _, idx := range []int{10, 110, 250} {
		dup := base[idx]
		at += 5_000_000
		dup.ArrivalNS = at
		ordered = append(ordered, dup)
	}
	return ordered
}

func burstLossTrace(rng *rand.Rand) []pwrtp.Captured {
	const n = 250
	out := make([]pwrtp.Captured, 0, n-10)
	for i := 0; i < n; i++ {
		if i >= 200 && i <= 209 { // 200 ms burst around 4.0-4.2 s, over loud tone
			continue
		}
		out = append(out, makeRTP(1000, i, 0, rng))
	}
	out[50], out[51] = out[51], out[50]
	return withArrivalJitter(out, rng)
}
