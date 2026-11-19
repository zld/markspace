package pipeline

import (
	"sort"
	"sync"
	"time"

	"mediakey/internal/license"
	"mediakey/internal/reason"
	"mediakey/internal/replay"
	"mediakey/internal/report"
)

// Runner wires packaging, validation, replay and boundary checks into one
// deterministic entry point used by the demo command.
type Runner struct{}

// Run executes one packaging and validation cycle and returns the report.
//
// The returned error is non-nil only when packaging could not complete, for
// example because of a duplicate nonce or invalid input. Validation failures
// are represented in the report and drive exit code 2 instead.
func (r *Runner) Run(cfg Config) (*report.Report, error) {
	pkg, err := Package(cfg)
	if err != nil {
		return nil, err
	}

	delivered := applyDeliveryFaults(pkg, cfg)
	// The primary validation consumes the license serial in its own cache. The
	// replay demonstration uses a separate cache so that it does not poison
	// the primary result and vice versa.
	out := Validate(pkg.Manifest, pkg.License, delivered, ValidateOptions{
		Secret:      pkg.Secret,
		Now:         cfg.Clock.Now(),
		Distributor: pkg.Distributor,
		Replay:      replay.New(),
		AssetID:     cfg.AssetID,
	})

	rep := &report.Report{
		AssetID:       cfg.AssetID,
		RotationCount: pkg.Manifest.RotationCount,
		License:       out.License,
		Manifest:      out.Manifest,
		Rotations:     nonNilRotations(pkg.Rotations),
		Keys:          keyFacts(pkg, cfg.Clock.Now()),
		Segments:      out.Segments,
	}
	if cfg.ReplayCheck {
		rep.Replay = checkReplay(pkg, cfg, delivered)
	}
	if cfg.AdvanceClock > 0 {
		rep.Boundary = checkBoundary(pkg, cfg, delivered)
	}
	rep.Finalize()
	return rep, nil
}

// applyDeliveryFaults returns a copy of the ciphertext map with the
// configured fault injected. Packaging output is never mutated.
func applyDeliveryFaults(pkg *Packaged, cfg Config) map[int][]byte {
	delivered := make(map[int][]byte, len(pkg.Ciphertexts))
	for idx, ct := range pkg.Ciphertexts {
		delivered[idx] = ct
	}
	if cfg.CorruptSegment >= 0 {
		if ct, ok := delivered[cfg.CorruptSegment]; ok {
			corrupted := make([]byte, len(ct))
			copy(corrupted, ct)
			corrupted[len(corrupted)/2] ^= 0xff
			delivered[cfg.CorruptSegment] = corrupted
		}
	}
	if cfg.DropSegment >= 0 {
		delete(delivered, cfg.DropSegment)
	}
	return delivered
}

// checkReplay presents the same license serial from many goroutines at once.
// Exactly one registration may succeed; every other attempt must be reported
// as LICENSE_REPLAY.
func checkReplay(pkg *Packaged, cfg Config, delivered map[int][]byte) report.ReplayResult {
	attempts := cfg.ReplayAttempts
	if attempts <= 0 {
		attempts = 4
	}
	cache := replay.New()
	var mu sync.Mutex
	reasons := make([]reason.Code, 0, attempts)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			outcome := Validate(pkg.Manifest, pkg.License, delivered, ValidateOptions{
				Secret:      pkg.Secret,
				Now:         cfg.Clock.Now(),
				Distributor: pkg.Distributor,
				Replay:      cache,
				AssetID:     cfg.AssetID,
			})
			mu.Lock()
			reasons = append(reasons, outcome.License.Reason)
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()

	res := report.ReplayResult{Checked: true, Serial: pkg.License.Serial, Attempts: attempts}
	sort.Slice(reasons, func(i, j int) bool { return reasons[i] < reasons[j] })
	for _, code := range reasons {
		switch code {
		case reason.NONE:
			res.Successes++
		case reason.LICENSE_REPLAY:
			res.Replays++
		}
	}
	res.SuccessReason = reason.NONE
	res.ReplayReason = reason.LICENSE_REPLAY
	res.Rejected = res.Successes == 1 && res.Replays == attempts-1
	return res
}

// checkBoundary advances the injected clock and re-validates twice: once with
// the original license, which must now be expired, and once with a freshly
// issued license, which isolates key expiry from license expiry. The clock is
// restored before returning.
func checkBoundary(pkg *Packaged, cfg Config, delivered map[int][]byte) report.BoundaryCheck {
	baseline := cfg.Clock.Offset()
	advanced := cfg.Clock.SetOffset(baseline + cfg.AdvanceClock)
	defer cfg.Clock.SetOffset(baseline)

	res := report.BoundaryCheck{
		Checked:    true,
		AdvancedBy: cfg.AdvanceClock.String(),
		Segments:   []report.SegmentResult{},
	}

	licenseOut := Validate(pkg.Manifest, pkg.License, delivered, ValidateOptions{
		Secret:      pkg.Secret,
		Now:         advanced,
		Distributor: pkg.Distributor,
		AssetID:     cfg.AssetID,
	})
	res.LicenseReason = licenseOut.License.Reason
	res.LicenseDetail = licenseOut.License.Detail
	res.Segments = licenseOut.Segments

	fresh, err := issueFresh(pkg, cfg, advanced)
	if err != nil {
		res.LicenseDetail = "fresh license could not be issued: " + err.Error()
		return res
	}
	keyOut := Validate(pkg.Manifest, fresh, delivered, ValidateOptions{
		Secret:      pkg.Secret,
		Now:         advanced,
		Distributor: pkg.Distributor,
		AssetID:     cfg.AssetID,
	})
	res.Segments = keyOut.Segments
	res.KeyProbeOK = countReason(keyOut.Segments, reason.NONE)
	res.KeyProbeFailed = len(keyOut.Segments) - res.KeyProbeOK
	return res
}

// issueFresh signs a new license with the same scope at the advanced instant,
// so key expiry can be observed independently of license expiry.
func issueFresh(pkg *Packaged, cfg Config, now time.Time) (*license.License, error) {
	signer, err := license.NewSigner(license.Options{
		Secret: pkg.Secret,
		Now:    func() time.Time { return now },
	})
	if err != nil {
		return nil, err
	}
	return signer.Issue(cfg.AssetID, pkg.KeyMin, pkg.KeyMax, cfg.LicenseTTL)
}

func countReason(results []report.SegmentResult, code reason.Code) int {
	n := 0
	for _, s := range results {
		if s.Reason == code {
			n++
		}
	}
	return n
}

// keyFacts snapshots key version history without exposing key material.
func keyFacts(pkg *Packaged, now time.Time) []report.KeyFact {
	history := pkg.Distributor.History()
	out := make([]report.KeyFact, 0, len(history))
	for _, k := range history {
		fact := report.KeyFact{
			KeyID:     k.ID,
			Version:   k.Version,
			CreatedAt: k.CreatedAt.UTC().Format(time.RFC3339),
			Active:    k.Active,
			Expired:   !k.ExpiresAt.IsZero() && !now.Before(k.ExpiresAt),
		}
		if !k.ExpiresAt.IsZero() {
			fact.ExpiresAt = k.ExpiresAt.UTC().Format(time.RFC3339)
		}
		out = append(out, fact)
	}
	return out
}

func nonNilRotations(in []report.Rotation) []report.Rotation {
	if in == nil {
		return []report.Rotation{}
	}
	return in
}
