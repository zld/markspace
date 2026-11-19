// Package license implements local, HMAC-signed content licenses. Signing
// uses a test-only key derived from a fixed seed; there is no network and no
// real authorization service anywhere in the tool.
package license

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"mediakey/internal/manifest"
)

const labelSigning = "mediakey/license-signing/v1/"

// DeriveTestSigningKey derives the deterministic test signing key for a
// license authority seed. Test material only; never a production key.
func DeriveTestSigningKey(seed []byte) []byte {
	mac := hmac.New(sha256.New, seed)
	mac.Write([]byte(labelSigning))
	return mac.Sum(nil)
}

// License scopes decryption to one asset, a set of key rounds, a contiguous
// chunk range and a validity window. All times are Unix seconds to keep the
// JSON deterministic and timezone-free.
type License struct {
	Version     int    `json:"version"`
	AssetID     string `json:"asset_id"`
	MinRound    int    `json:"min_round"`
	MaxRound    int    `json:"max_round"`
	MinChunk    int    `json:"min_chunk"`
	MaxChunk    int    `json:"max_chunk"`
	NotBefore   int64  `json:"not_before"`
	NotAfter    int64  `json:"not_after"`
	GraceWindow int64  `json:"grace_window_seconds"`
	Sig         []byte `json:"sig"`
}

func signingPayload(l *License) []byte {
	cp := *l
	cp.Sig = nil
	raw, err := json.Marshal(cp)
	if err != nil {
		panic("license payload must marshal: " + err.Error())
	}
	return raw
}

func sign(payload, signingKey []byte) []byte {
	mac := hmac.New(sha256.New, signingKey)
	mac.Write(payload)
	return mac.Sum(nil)
}

// Sign fills the signature field in place. The caller owns key lifecycle.
func (l *License) Sign(signingKey []byte) error {
	if len(signingKey) != sha256.Size {
		return manifest.NewError(manifest.ReasonInvalidInput, "signing key must be 32 bytes")
	}
	l.Version = manifest.FormatVersion
	l.Sig = sign(signingPayload(l), signingKey)
	return nil
}

// RoundAllowed reports whether round falls inside the licensed rounds.
func (l *License) RoundAllowed(round int) bool {
	return round >= l.MinRound && round <= l.MaxRound
}

// CheckOrder records the categories Verify distinguishes, in the order they
// are evaluated: signature first, then asset, scope and finally time.
var CheckOrder = []manifest.FailureReason{
	manifest.ReasonLicenseSignature,
	manifest.ReasonAssetMismatch,
	manifest.ReasonRangeExceeded,
	manifest.ReasonNotYetValid,
	manifest.ReasonExpired,
}

// Verify validates signature, asset binding, round/chunk scope and the time
// window. now is injectable so tests and demos can reproduce expiry
// deterministically.
func (l *License) Verify(signingKey []byte, assetID string, round, chunkIndex int, now time.Time) error {
	if l == nil {
		return manifest.NewError(manifest.ReasonInvalidInput, "nil license")
	}
	expected := sign(signingPayload(l), signingKey)
	if !hmac.Equal(expected, l.Sig) {
		return manifest.NewError(manifest.ReasonLicenseSignature, "license signature mismatch")
	}
	if l.AssetID != assetID {
		return manifest.NewError(manifest.ReasonAssetMismatch,
			fmt.Sprintf("license for %q, asset %q", l.AssetID, assetID))
	}
	if !l.RoundAllowed(round) || chunkIndex < l.MinChunk || chunkIndex > l.MaxChunk {
		return manifest.NewError(manifest.ReasonRangeExceeded,
			fmt.Sprintf("round %d/chunk %d outside licensed scope", round, chunkIndex))
	}
	ts := now.Unix()
	if ts < l.NotBefore {
		return manifest.NewError(manifest.ReasonNotYetValid, "license not yet valid")
	}
	if ts > l.NotAfter {
		return manifest.NewError(manifest.ReasonExpired, "license expired")
	}
	return nil
}

// ExpiredWithGrace mirrors Verify but tolerates an expired license for up to
// GraceWindow seconds, modeling the rotation overlap during which old rounds
// may still complete in-flight verification.
//
//   - inside the window: expiry is allowed and ok=true is returned;
//   - beyond the window: reason key_expired is returned;
//   - every other failure category is reported exactly as Verify would.
func (l *License) ExpiredWithGrace(signingKey []byte, assetID string, round, chunkIndex int, now time.Time) error {
	err := l.Verify(signingKey, assetID, round, chunkIndex, now)
	if err == nil {
		return nil
	}
	if manifest.ErrorCode(err) != manifest.ReasonExpired {
		return err
	}
	if now.Unix() <= l.NotAfter+l.GraceWindow {
		return nil // overlap window: old round may still finish
	}
	return manifest.NewError(manifest.ReasonKeyExpired, "old key round used outside overlap window")
}
