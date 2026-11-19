package license

import (
	"testing"
	"time"

	"mediakey/internal/manifest"
)

func signingKey() []byte { return DeriveTestSigningKey([]byte("license-unit-seed")) }

func baseLicense() *License {
	return &License{
		AssetID:   "asset-9",
		MinRound:  0,
		MaxRound:  3,
		MinChunk:  0,
		MaxChunk:  11,
		NotBefore: 1000,
		NotAfter:  2000,
	}
}

func TestVerifyAllFailureCategories(t *testing.T) {
	key := signingKey()
	now := time.Unix(1500, 0)

	good := baseLicense()
	if err := good.Sign(key); err != nil {
		t.Fatal(err)
	}
	if err := good.Verify(key, "asset-9", 2, 5, now); err != nil {
		t.Fatalf("valid license rejected: %v", err)
	}

	// Signature error: flip a signature byte.
	badSig := *good
	badSig.Sig = append([]byte(nil), good.Sig...)
	badSig.Sig[0] ^= 0xFF
	if code := manifest.ErrorCode(badSig.Verify(key, "asset-9", 2, 5, now)); code != manifest.ReasonLicenseSignature {
		t.Fatalf("signature code = %s", code)
	}
	// Asset mismatch.
	if code := manifest.ErrorCode(good.Verify(key, "other-asset", 2, 5, now)); code != manifest.ReasonAssetMismatch {
		t.Fatalf("asset code = %s", code)
	}
	// Round and chunk out of scope.
	if code := manifest.ErrorCode(good.Verify(key, "asset-9", 9, 5, now)); code != manifest.ReasonRangeExceeded {
		t.Fatalf("round code = %s", code)
	}
	if code := manifest.ErrorCode(good.Verify(key, "asset-9", 2, 99, now)); code != manifest.ReasonRangeExceeded {
		t.Fatalf("chunk code = %s", code)
	}
	// Not yet valid and expired.
	if code := manifest.ErrorCode(good.Verify(key, "asset-9", 2, 5, time.Unix(500, 0))); code != manifest.ReasonNotYetValid {
		t.Fatalf("not-yet-valid code = %s", code)
	}
	if code := manifest.ErrorCode(good.Verify(key, "asset-9", 2, 5, time.Unix(2500, 0))); code != manifest.ReasonExpired {
		t.Fatalf("expired code = %s", code)
	}
}

func TestGraceWindowBoundary(t *testing.T) {
	key := signingKey()
	lic := baseLicense()
	lic.GraceWindow = 100
	if err := lic.Sign(key); err != nil {
		t.Fatal(err)
	}
	// Just inside the overlap window: expiry tolerated.
	if err := lic.ExpiredWithGrace(key, "asset-9", 1, 1, time.Unix(2050, 0)); err != nil {
		t.Fatalf("inside grace should pass: %v", err)
	}
	// Beyond the window: key_expired, distinct from plain expiry.
	if code := manifest.ErrorCode(lic.ExpiredWithGrace(key, "asset-9", 1, 1, time.Unix(2101, 0))); code != manifest.ReasonKeyExpired {
		t.Fatalf("outside grace code = %s, want key_expired", code)
	}
	// Non-expiry failures (e.g. asset mismatch) are never masked by grace.
	if code := manifest.ErrorCode(lic.ExpiredWithGrace(key, "wrong", 1, 1, time.Unix(2050, 0))); code != manifest.ReasonAssetMismatch {
		t.Fatalf("grace masked asset mismatch: %s", code)
	}
}
