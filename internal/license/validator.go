// Package license issues and verifies simulated content licenses. Licenses
// bind the package identifier, shard range, validity window and a unique
// nonce, and carry a local HMAC-SHA256 signature. No authorization service
// is contacted: the simulator is the only signer.
package license

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"mediakey/internal/model"
)

var (
	// ErrExpired means the license validity window has ended.
	ErrExpired = errors.New("license expired")
	// ErrScope means the shard lies outside the licensed range or package.
	ErrScope = errors.New("license scope mismatch")
	// ErrSignature means the HMAC verification failed.
	ErrSignature = errors.New("license signature mismatch")
)

// Validator signs licenses with one process-local secret and verifies them
// against an injectable clock.
type Validator struct {
	secret []byte
	clock  model.Clock
}

// New builds a Validator with the given HMAC secret and clock.
func New(secret []byte, clock model.Clock) *Validator {
	cp := append([]byte(nil), secret...)
	return &Validator{secret: cp, clock: clock}
}

// signPayload is the canonical byte string covered by the HMAC. It uses
// length-prefixed fields so no two different licenses can collide.
func signPayload(l model.License) []byte {
	out := make([]byte, 0, 128)
	put := func(s string) {
		var lenBuf [4]byte
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(s)))
		out = append(out, lenBuf[:]...)
		out = append(out, s...)
	}
	putInt := func(v int64) {
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(v))
		out = append(out, buf[:]...)
	}
	put(l.PackageID)
	put(l.Nonce)
	putInt(int64(l.ShardID))
	putInt(int64(l.ShardFrom))
	putInt(int64(l.ShardTo))
	putInt(l.IssuedAt)
	putInt(l.ExpiresAt)
	return out
}

// sign returns the hex HMAC-SHA256 of the license payload.
func (v *Validator) sign(l model.License) string {
	mac := hmac.New(sha256.New, v.secret)
	mac.Write(signPayload(l))
	return hex.EncodeToString(mac.Sum(nil))
}

// NewNonce returns a random 16-byte hex nonce.
func NewNonce() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("license nonce: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Issue creates a signed license covering shardID (and the inclusive range
// [from,to], which normally equals shardID) with a fixed lifetime.
func (v *Validator) Issue(packageID string, shardID, from, to int, ttl time.Duration, nonce string) (model.License, error) {
	if nonce == "" {
		var err error
		nonce, err = NewNonce()
		if err != nil {
			return model.License{}, err
		}
	}
	now := v.clock.Now()
	l := model.License{
		PackageID: packageID,
		ShardID:   shardID,
		ShardFrom: from,
		ShardTo:   to,
		Nonce:     nonce,
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(ttl).Unix(),
	}
	l.Sig = v.sign(l)
	return l, nil
}

// WithClock returns a copy of the validator using a different clock while
// keeping the same signing secret, so delayed shards can be validated at
// their own point in time.
func (v *Validator) WithClock(clock model.Clock) *Validator {
	return &Validator{secret: append([]byte(nil), v.secret...), clock: clock}
}

// SignExternal recomputes the signature for an externally modified license
// (for example one whose expiry has been back-dated by a fixture).
func (v *Validator) SignExternal(l model.License) string {
	return v.sign(l)
}

// Verify checks scope, validity window and signature in that order. Replay
// detection is intentionally not part of this method: the ledger owns it so
// that the order of checks stays centralized in the pipeline.
func (v *Validator) Verify(packageID string, shardID int, l model.License) error {
	if l.PackageID != packageID || shardID < l.ShardFrom || shardID > l.ShardTo || l.ShardID != shardID {
		return ErrScope
	}
	if v.clock.Now().Unix() >= l.ExpiresAt {
		return ErrExpired
	}
	expected := v.sign(l)
	if !hmac.Equal([]byte(expected), []byte(l.Sig)) {
		return ErrSignature
	}
	return nil
}
