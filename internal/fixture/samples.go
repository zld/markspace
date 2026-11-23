package fixture

import (
	"mediatool/internal/clock"
)

// FaultClass names one of the self-generated fault samples.
type FaultClass string

const (
	FaultReorderDuplicate FaultClass = "reorder_duplicate"
	FaultTruncation       FaultClass = "truncation"
	FaultRTCPRegression   FaultClass = "rtcp_regression"
	FaultDigestMismatch   FaultClass = "index_digest_mismatch"
)

// Sample is one named fault fixture plus its expected located evidence.
type Sample struct {
	Class FaultClass
	// Options produce the sample.
	Options Options
	// ExpectKind is the fault kind the scanner must report first.
	ExpectKind string
	// ExpectOffset is the true byte offset of the damage, when known.
	ExpectOffset int64
	// Digest is the index digest the fixture is expected to produce.
	Digest string
}

// Samples generates the four required fault classes with small, fast captures.
func Samples() ([]Sample, error) {
	base := DemoOptions(99, 2, 1, 0, 0)
	base.DurationMs = 2000
	base.SRIntervalMs = 500

	reorder := base
	reorder.InjectReorder = 2
	reorder.InjectDuplicate = 1

	trunc := base
	trunc.InjectTruncate = 1

	regress := base
	regress.InjectRTCPRegression = true
	regress.RTCPRegressionTrack = 1
	regress.RTCPRegressionAtMs = 1000

	var out []Sample
	for _, item := range []struct {
		class  FaultClass
		opts   Options
		kind   string
		offset func(*Capture) int64
	}{
		{FaultReorderDuplicate, reorder, "reordered", nil},
		{FaultTruncation, trunc, "truncated", func(c *Capture) int64 { return c.FirstTruncation }},
		{FaultRTCPRegression, regress, "rtcp_invalid", nil},
	} {
		cap, err := Generate(item.opts)
		if err != nil {
			return nil, err
		}
		s := Sample{Class: item.class, Options: item.opts, ExpectKind: item.kind,
			Digest: DigestOf(cap.Bytes)}
		if item.offset != nil {
			s.ExpectOffset = item.offset(cap)
		}
		out = append(out, s)
	}

	// The digest-mismatch sample has no injected media damage: it is the same
	// clean capture processed twice, with the second pass bound to a stale
	// index digest.
	clean, err := Generate(base)
	if err != nil {
		return nil, err
	}
	out = append(out, Sample{
		Class:      FaultDigestMismatch,
		Options:    base,
		ExpectKind: "index_digest",
		Digest:     DigestOf(clean.Bytes),
	})
	return out, nil
}

// NTPOf exposes the fixture epoch for tests that need absolute SR times.
func NTPOf(ms int64) clock.NTP { return clock.NsToNTP(EpochNs + ms*1e6) }
