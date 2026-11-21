// Package pipeline runs the publish-verification flow: key assignment is
// computed once in shard-id order, then shards are processed in parallel
// workers and the report is reassembled in shard-id order. A single shard
// failure never aborts the package check.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"mediakey/internal/crypto"
	"mediakey/internal/distributor"
	"mediakey/internal/fixture"
	"mediakey/internal/license"
	"mediakey/internal/model"
	"mediakey/internal/replay"
)

// LicenseTTL is the lifetime of an issued, non-expired license.
const LicenseTTL = 30 * time.Minute

// Runner holds the collaborators for one pipeline run.
type Runner struct {
	clock       model.Clock
	rotator     *distributor.Rotator
	validator   *license.Validator
	ledger      *replay.Ledger
	rotateEvery int
	workers     int
	keyTTL      time.Duration
}

// Config configures a Runner. Zero values are replaced with sane defaults.
type Config struct {
	Clock       model.Clock
	RotateEvery int
	Workers     int
	KeyTTL      time.Duration
}

// New constructs a Runner for one package.
func New(packageID string, cfg Config) *Runner {
	if cfg.Clock == nil {
		cfg.Clock = model.FixedClock{T: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	}
	if cfg.RotateEvery < 1 {
		cfg.RotateEvery = 4
	}
	if cfg.Workers < 1 {
		cfg.Workers = 1
	}
	if cfg.KeyTTL <= 0 {
		cfg.KeyTTL = distributor.DefaultTTL
	}
	secret := []byte("local-hmac-secret-" + packageID)
	return &Runner{
		clock:       cfg.Clock,
		rotator:     distributor.New(cfg.Clock, packageID, cfg.RotateEvery, cfg.KeyTTL),
		validator:   license.New(secret, cfg.Clock),
		ledger:      replay.New(),
		rotateEvery: cfg.RotateEvery,
		workers:     cfg.Workers,
		keyTTL:      cfg.KeyTTL,
	}
}

// RotationCount exposes the distributor rotation counter.
func (r *Runner) RotationCount() int { return r.rotator.RotationCount() }

// Ledger exposes the nonce ledger (used by tests and fixture seeding).
func (r *Runner) Ledger() *replay.Ledger { return r.ledger }

// work is the immutable per-shard input handed to a worker.
type work struct {
	shard   model.Shard
	plain   []byte
	fault   string
	clock   model.Clock
	license model.License
	packet  *crypto.Packet
}

// Run processes every shard of the sample and returns an ordered report.
func (r *Runner) Run(sample *fixture.Sample) (*model.Report, error) {
	man := sample.Manifest
	items, err := r.prepare(sample)
	if err != nil {
		return nil, err
	}

	results := make([]model.ShardResult, len(items))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < r.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i] = r.process(man.PackageID, man.Digest, items[i])
			}
		}()
	}
	for i := range items {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	rep := &model.Report{
		PackageID:     man.PackageID,
		RotationCount: r.rotator.RotationCount(),
		ShardCount:    len(results),
		Results:       results,
	}
	for _, res := range results {
		if res.Status == model.StatusAccepted {
			rep.Accepted++
		} else {
			rep.Rejected++
		}
	}
	return rep, nil
}

// prepare performs the ordered, deterministic half of the work: stable key
// assignment and per-shard encryption plus license issuance.
func (r *Runner) prepare(sample *fixture.Sample) ([]*work, error) {
	man := sample.Manifest
	items := make([]*work, 0, len(man.Shards))
	for _, sh := range man.Shards {
		keyID, err := r.rotator.KeyIDFor(sh.ShardID)
		if err != nil {
			return nil, err
		}
		fault := sample.Fault(sh.ShardID)
		clock := r.clock
		// A delayed shard validates after its key has already expired.
		if fault == fixture.FaultKeyExpired {
			clock = model.OffsetClock{Base: r.clock, Offset: r.keyTTL + time.Minute}
		}

		nonce, err := license.NewNonce()
		if err != nil {
			return nil, err
		}
		if fault == fixture.FaultLicenseReplay {
			r.ledger.Seed(nonce)
		}
		lic, err := r.validator.Issue(man.PackageID, sh.ShardID, sh.ShardID, sh.ShardID, LicenseTTL, nonce)
		if err != nil {
			return nil, err
		}
		if fault == fixture.FaultLicenseExpired {
			lic.ExpiresAt = r.clock.Now().Unix() - 1
			lic.Sig = r.validator.SignExternal(lic)
		}

		key, err := r.rotator.Material(keyID)
		if err != nil {
			return nil, err
		}
		pkt, err := crypto.Seal(man.PackageID, keyID, key, sh.ShardID, man.Digest, sample.Plaintexts[sh.ShardID])
		if err != nil {
			return nil, err
		}
		if fault == fixture.FaultAuthTag {
			crypto.Corrupt(pkt)
		}
		items = append(items, &work{
			shard:   sh,
			plain:   sample.Plaintexts[sh.ShardID],
			fault:   fault,
			clock:   clock,
			license: lic,
			packet:  pkt,
		})
	}
	return items, nil
}

// process applies the fixed validation order for one shard and maps every
// failure to a stable reason. It never retries decryption with another key.
func (r *Runner) process(packageID, manifestDigest string, item *work) (res model.ShardResult) {
	sh := item.shard
	res = model.ShardResult{ShardID: sh.ShardID, KeyID: item.packet.KeyID}
	reject := func(reason, detail string) model.ShardResult {
		res.Status = model.StatusRejected
		res.FailureReason = reason
		res.Detail = detail
		return res
	}

	// 1. Key existence and expiry at this shard's own clock.
	key, err := r.rotator.Material(item.packet.KeyID)
	if err != nil {
		return reject(model.ReasonUnknownKey, err.Error())
	}
	if item.clock != r.clock {
		if expired := r.rotator.IsExpiredAt(item.packet.KeyID, item.clock); expired {
			return reject(model.ReasonKeyExpired, "key validity window ended before delayed shard arrived")
		}
	} else if r.rotator.IsExpired(item.packet.KeyID) {
		return reject(model.ReasonKeyExpired, "key validity window ended")
	}

	// 2. License scope, expiry, signature.
	v := r.validator.WithClock(item.clock)
	if err := v.Verify(packageID, sh.ShardID, item.license); err != nil {
		if errors.Is(err, license.ErrExpired) {
			return reject(model.ReasonLicenseExpired, err.Error())
		}
		if errors.Is(err, license.ErrScope) {
			return reject(model.ReasonUnknownKey, err.Error())
		}
		return reject(model.ReasonAuthTag, err.Error())
	}

	// 3. Replay ledger: a nonce passes at most once, even concurrently.
	if !r.ledger.Consume(item.license.Nonce) {
		return reject(model.ReasonLicenseReplay, "license nonce was already consumed")
	}

	// 4. Authenticated decryption (manifest binding, tag, length).
	if item.fault == fixture.FaultManifest {
		manifestDigest = manifestDigest[:len(manifestDigest)-1] + "0"
	}
	plain, reason, err := crypto.Open(packageID, key, sh.ShardID, manifestDigest, sh.Size, item.packet)
	if err != nil {
		return reject(reason, err.Error())
	}
	// 5. Content integrity: the decrypted bytes must hash to the value
	// recorded in the manifest. A tampered plaintext bundle is caught here.
	sum := sha256.Sum256(plain)
	if hex.EncodeToString(sum[:]) != sh.PlaintextHash {
		return reject(model.ReasonManifest, "decrypted plaintext hash differs from manifest")
	}
	res.Status = model.StatusAccepted
	res.FailureReason = model.ReasonNone
	return res
}
