// Package pipeline orchestrates segment processing: atomic replay
// registration in input order, key expiry checks, license validation and
// concurrent authenticated decryption, producing deterministically ordered
// records.
package pipeline

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"mediaseal/internal/contentcrypt"
	"mediaseal/internal/fixture"
	"mediaseal/internal/keys"
	"mediaseal/internal/license"
	"mediaseal/internal/media"
)

// maxWorkers bounds the number of concurrent decryption workers.
const maxWorkers = 8

// Injection selects which fault to inject at which segment index.
type Injection struct {
	Tamper         map[int]bool
	Replay         map[int]bool
	ExpiredKey     map[int]bool
	ExpiredLicense map[int]bool
}

// Config controls one pipeline run.
type Config struct {
	AssetID     string
	Segments    int
	RotateEvery int
	Inject      Injection
	Clock       media.Clock
}

// Result carries the run outcome. It contains no key material, no private
// keys and no license nonces.
type Result struct {
	AssetID       string
	Records       []media.Record
	Keys          []media.KeyInfo
	RotationCount int
	// Archive holds ciphertext artifacts (nonce || ciphertext) per accepted
	// segment ID; only ciphertext is ever persisted.
	Archive map[string][]byte
}

// request is one prepared segment submission.
type request struct {
	seg        media.Segment
	keyID      string
	license    media.License
	ciphertext []byte // nonce || ciphertext
}

// Run executes the full pipeline and returns the ordered result.
func Run(cfg Config) (*Result, error) {
	if cfg.Segments < 1 {
		return nil, fmt.Errorf("segments must be >= 1")
	}
	if cfg.Clock == nil {
		return nil, fmt.Errorf("clock is required")
	}
	clock := cfg.Clock
	dist := keys.NewDistributor(cfg.RotateEvery, clock)
	issuer, err := license.NewIssuer(clock)
	if err != nil {
		return nil, err
	}
	segments := fixture.Generate(cfg.AssetID, cfg.Segments)

	// Prepare requests in input order, injecting faults as configured.
	reqs := make([]request, 0, len(segments)+len(cfg.Inject.Replay))
	for _, seg := range segments {
		info, err := dist.KeyForSegment(seg.Index)
		if err != nil {
			return nil, err
		}
		lic, err := issue(issuer, seg, info.KeyID, cfg.Inject.ExpiredLicense[seg.Index])
		if err != nil {
			return nil, err
		}
		key, _, _ := dist.Lookup(info.KeyID)
		nonce, ct, err := contentcrypt.Encrypt(key, seg.Plaintext, contentcrypt.AAD(seg, info.KeyID))
		if err != nil {
			return nil, err
		}
		blob := append(append([]byte{}, nonce...), ct...)
		if cfg.Inject.Tamper[seg.Index] {
			blob[len(blob)-1] ^= 0xFF
		}
		req := request{seg: seg, keyID: info.KeyID, license: lic, ciphertext: blob}
		reqs = append(reqs, req)
		if cfg.Inject.Replay[seg.Index] {
			reqs = append(reqs, req) // identical resubmission of the same request
		}
	}
	for idx := range cfg.Inject.ExpiredKey {
		dist.ExpireKeyForSegment(idx)
	}

	// Register (license nonce, segment) pairs atomically in input order;
	// duplicates are rejected as replays before any decryption is scheduled.
	records := make([]media.Record, len(reqs))
	seen := make(map[string]struct{}, len(reqs))
	var tasks []int
	for pos, req := range reqs {
		token := req.license.Nonce + "|" + req.seg.ID()
		if _, dup := seen[token]; dup {
			records[pos] = media.Record{
				SegmentID:     req.seg.ID(),
				KeyID:         req.keyID,
				Status:        media.StatusRejected,
				FailureReason: media.ReasonReplay,
			}
			continue
		}
		seen[token] = struct{}{}
		tasks = append(tasks, pos)
	}

	// Concurrent decryption: at most maxWorkers workers fed by a bounded
	// queue; results land in their input-order slots.
	workerCount := maxWorkers
	if len(tasks) < workerCount {
		workerCount = len(tasks)
	}
	queue := make(chan int, maxWorkers)
	var wg sync.WaitGroup
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pos := range queue {
				records[pos] = process(dist, issuer, reqs[pos], clock)
			}
		}()
	}
	for _, pos := range tasks {
		queue <- pos
	}
	close(queue)
	wg.Wait()

	res := &Result{
		AssetID:       cfg.AssetID,
		Records:       records,
		Keys:          dist.Infos(),
		RotationCount: dist.RotationCount(),
		Archive:       make(map[string][]byte),
	}
	for pos, req := range reqs {
		if records[pos].Status == media.StatusAccepted {
			res.Archive[req.seg.ID()] = req.ciphertext
		}
	}
	if err := res.checkInvariants(); err != nil {
		return nil, err
	}
	return res, nil
}

func issue(issuer *license.Issuer, seg media.Segment, keyID string, expired bool) (media.License, error) {
	if expired {
		return issuer.IssueExpired(seg, keyID)
	}
	return issuer.Issue(seg, keyID)
}

// process runs the per-segment checks: key expiry, license validity,
// authenticated decryption and plaintext digest comparison.
func process(dist *keys.Distributor, issuer *license.Issuer, req request, clock media.Clock) media.Record {
	rec := media.Record{
		SegmentID:     req.seg.ID(),
		KeyID:         req.keyID,
		Status:        media.StatusAccepted,
		FailureReason: media.ReasonNone,
	}
	fail := func(reason string) media.Record {
		rec.Status = media.StatusRejected
		rec.FailureReason = reason
		return rec
	}
	key, info, ok := dist.Lookup(req.keyID)
	if !ok {
		return fail(media.ReasonUnknownKey)
	}
	if now := clock.Now(); now.Before(info.NotBefore) || !now.Before(info.NotAfter) {
		return fail(media.ReasonKeyExpired)
	}
	if reason := issuer.Validate(req.license, req.seg); reason != media.ReasonNone {
		return fail(reason)
	}
	if len(req.ciphertext) < contentcrypt.NonceSize {
		return fail(media.ReasonAuthFailed)
	}
	nonce := req.ciphertext[:contentcrypt.NonceSize]
	ct := req.ciphertext[contentcrypt.NonceSize:]
	plain, err := contentcrypt.Decrypt(key, nonce, ct, contentcrypt.AAD(req.seg, req.keyID))
	if err != nil {
		return fail(media.ReasonAuthFailed)
	}
	if sha256.Sum256(plain) != req.seg.Digest {
		return fail(media.ReasonDigestMismatch)
	}
	return rec
}

// checkInvariants guards internal consistency; a violation maps to exit
// code 3 at the CLI boundary.
func (r *Result) checkInvariants() error {
	if len(r.Records) == 0 {
		return fmt.Errorf("invariant: no records produced")
	}
	for i, rec := range r.Records {
		if rec.SegmentID == "" || rec.KeyID == "" {
			return fmt.Errorf("invariant: record %d missing identifiers", i)
		}
		if rec.Status == media.StatusAccepted && rec.FailureReason != "" {
			return fmt.Errorf("invariant: accepted record %d has failure reason", i)
		}
		if rec.Status == media.StatusRejected && rec.FailureReason == "" {
			return fmt.Errorf("invariant: rejected record %d lacks failure reason", i)
		}
	}
	if r.RotationCount != len(r.Keys)-1 {
		return fmt.Errorf("invariant: rotation count %d inconsistent with %d keys",
			r.RotationCount, len(r.Keys))
	}
	return nil
}
