// Package fixture synthesises MPEG-TS test streams in memory. No external
// media is used: PAT/PMT, PCR, PES samples and corruption are all generated
// deterministically from a seed.
package fixture

import (
	"math/rand"

	"tsrepair/internal/clock"
	"tsrepair/internal/ts"
)

// Corruption selects an injectable fault. Multiple faults can be OR-ed by
// passing several Corruption values to Options.
type Corruption string

const (
	// CorruptCounterGap makes one PID skip a continuity counter value.
	CorruptCounterGap Corruption = "counter-gap"
	// CorruptPCRWrap forces a PCR value near the 42-bit wrap boundary.
	CorruptPCRWrap Corruption = "pcr-wrap"
	// CorruptPSIVersion emits a new PAT version followed by a rollback.
	CorruptPSIVersion Corruption = "psi-version"
	// CorruptTruncatedPacket cuts the final packet to fewer than 188 bytes.
	CorruptTruncatedPacket Corruption = "truncated-packet"
	// CorruptMissingPCRAnchor omits all PCR adaptation fields.
	CorruptMissingPCRAnchor Corruption = "missing-pcr-anchor"
	// CorruptPSIMissingSection advertises a last_section_number that never
	// fully arrives.
	CorruptPSIMissingSection Corruption = "psi-missing-section"
)

// Options configures one generated stream.
type Options struct {
	Seed              int64
	Segments          int
	Representations   int
	SamplesPerSegment int
	VideoPID          uint16
	PCRPID            uint16
	PMTPID            uint16
	Corruptions       []Corruption
}

// DefaultOptions returns a coherent single-representation configuration.
func DefaultOptions(seed int64) Options {
	return Options{
		Seed:              seed,
		Segments:          4,
		Representations:   1,
		SamplesPerSegment: 8,
		VideoPID:          0x0100,
		PCRPID:            0x0100,
		PMTPID:            0x1000,
	}
}

func has(cs []Corruption, want Corruption) bool {
	for _, c := range cs {
		if c == want {
			return true
		}
	}
	return false
}

// Representation is one generated bitrate: its raw TS bytes plus the ground
// truth a repair pipeline must recover.
type Representation struct {
	ID       int
	VideoPID uint16
	PCRPID   uint16
	PMTPID   uint16
	Data     []byte
	// ExpectedPTS90k are the ground-truth sample PTS values (unwrapped).
	ExpectedPTS90k []int64
	// KeyframeSample indexes (0-based) that carry random-access markers.
	KeyframeSamples []int
	// PCRRaw holds every emitted raw PCR in order.
	PCRRaw []int64
}

// Build generates one stream per representation. Representations share the
// same media timing so the fan-in boundary check should agree.
func Build(opts Options) ([]*Representation, error) {
	rng := rand.New(rand.NewSource(opts.Seed))
	var out []*Representation
	for rep := 0; rep < opts.Representations; rep++ {
		b := &builder{
			opts: opts,
			rng:  rng,
			rep:  rep,
			cc:   map[uint16]uint8{},
		}
		rep2, err := b.build()
		if err != nil {
			return nil, err
		}
		out = append(out, rep2)
	}
	return out, nil
}

type builder struct {
	opts Options
	rng  *rand.Rand
	rep  int
	cc   map[uint16]uint8
	out  []byte
}

func (b *builder) nextCC(pid uint16) uint8 {
	v := b.cc[pid]
	b.cc[pid] = (v + 1) & 0x0F
	return v
}

func (b *builder) emit(p []byte) { b.out = append(b.out, p...) }

// build constructs one representation.
func (b *builder) build() (*Representation, error) {
	o := b.opts
	rep := &Representation{
		ID:       b.rep,
		VideoPID: o.VideoPID,
		PCRPID:   o.PCRPID,
		PMTPID:   o.PMTPID,
	}

	// PAT then PMT, each in a PUSI packet with a zero pointer field.
	pat := b.buildPAT(1)
	if has(o.Corruptions, CorruptPSIMissingSection) {
		// Advertise a second section that is never sent, then recompute the
		// CRC so the failure is genuinely a missing section, not a CRC error.
		pat[7] = 1
		pat = appendCRC(pat)
	}
	b.emitSection(ts.PIDPAT, pat)
	pmt := b.buildPMT()
	b.emitSection(o.PMTPID, pmt)

	// For the version corruption, emit a higher PAT version and then a
	// rollback to the original; the rollback must be rejected downstream.
	if has(o.Corruptions, CorruptPSIVersion) {
		b.emitSection(ts.PIDPAT, b.buildPAT(2))
		b.emitSection(ts.PIDPAT, b.buildPAT(1))
	}

	omitPCR := has(o.Corruptions, CorruptMissingPCRAnchor)
	totalSamples := o.Segments * o.SamplesPerSegment
	// PCR starts one second in and advances one frame interval per sample.
	pcrBase := int64(90_000)
	pts := int64(0)
	frameDelta := int64(3003) // ~29.97 fps, non-integer so VFR-safe spacing

	for sample := 0; sample < totalSamples; sample++ {
		keyframe := sample%o.SamplesPerSegment == 0
		if keyframe {
			rep.KeyframeSamples = append(rep.KeyframeSamples, sample)
		}
		rep.ExpectedPTS90k = append(rep.ExpectedPTS90k, pts)

		// PCR packet (adaptation + payload) carrying the current PCR.
		if !omitPCR {
			rawPCR := pcrBase*300 + (int64(sample) % 300)
			rep.PCRRaw = append(rep.PCRRaw, rawPCR)
			b.emitPCRPacket(o.PCRPID, rawPCR, keyframe)

			// On the final sample, also emit a near-boundary PCR followed by
			// a wrapped PCR so the unwrapper must cross the 42-bit modulus.
			if has(o.Corruptions, CorruptPCRWrap) && sample == totalSamples-1 {
				near := clock.PCRMod - 300
				rep.PCRRaw = append(rep.PCRRaw, near, 150)
				b.emitPCRPacket(o.PCRPID, near, keyframe)
				b.emitPCRPacket(o.PCRPID, 150, false)
			}
		}

		// PES video packet.
		// Halfway through, skip one video-PID continuity value to create a
		// real gap that the parser must detect.
		if has(o.Corruptions, CorruptCounterGap) && sample == totalSamples/2 {
			b.cc[o.VideoPID] = (b.cc[o.VideoPID] + 1) & 0x0F
		}
		pes := b.buildPES(pts, keyframe)
		b.emitPESPacket(o.VideoPID, pes, keyframe)

		pts += frameDelta
		pcrBase += frameDelta
	}

	// Truncated final packet: append a short tail whose first byte is not a
	// sync byte so the reader reports sync loss / truncation rather than
	// silently scanning past it.
	if has(o.Corruptions, CorruptTruncatedPacket) {
		tail := make([]byte, 50)
		for i := range tail {
			tail[i] = 0xAB
		}
		b.emit(tail)
	}

	rep.Data = b.out
	return rep, nil
}
