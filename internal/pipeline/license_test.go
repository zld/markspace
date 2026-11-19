package pipeline

import (
	"strings"
	"testing"
	"time"

	"mediakey/internal/license"
	"mediakey/internal/reason"
	"mediakey/internal/replay"
	"mediakey/internal/report"
)

func TestValidateExpiredLicenseBlocksAllSegments(t *testing.T) {
	h := newHarness(t, 6, 3)
	out := Validate(h.pkg.Manifest, h.pkg.License, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base.Add(time.Minute), Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	if out.License.Reason != reason.LICENSE_EXPIRED {
		t.Fatalf("license reason = %s", out.License.Reason)
	}
	for _, s := range out.Segments {
		if s.Reason != reason.LICENSE_EXPIRED {
			t.Fatalf("segment %d reason = %s, want LICENSE_EXPIRED", s.SegmentIndex, s.Reason)
		}
	}
}

func TestValidateExpiredKeyIsTerminal(t *testing.T) {
	h := newHarness(t, 6, 3)
	// A fresh license at a later time still fails because the key versions
	// themselves are expired; there is no fallback to another version.
	later := h.base.Add(2 * time.Minute)
	signer, err := license.NewSigner(license.Options{
		Secret: h.pkg.Secret, Now: func() time.Time { return later },
	})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := signer.Issue(h.cfg.AssetID, 1, 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	out := Validate(h.pkg.Manifest, fresh, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: later, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	if out.License.Reason != reason.NONE {
		t.Fatalf("fresh license rejected: %+v", out.License)
	}
	for _, s := range out.Segments {
		if s.Reason != reason.KEY_EXPIRED {
			t.Fatalf("segment %d reason = %s, want KEY_EXPIRED", s.SegmentIndex, s.Reason)
		}
	}
}

func TestValidateUnknownKeyIsReported(t *testing.T) {
	h := newHarness(t, 3, 3)
	m := cloneManifest(h.pkg.Manifest)
	m.Segments[1].KeyID = "k9"
	out := Validate(m, h.pkg.License, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	if out.Segments[1].Reason != reason.KEY_UNKNOWN {
		t.Fatalf("segment 1 reason = %s, want KEY_UNKNOWN", out.Segments[1].Reason)
	}
	if out.Segments[0].Status != report.StatusOK || out.Segments[2].Status != report.StatusOK {
		t.Fatal("healthy segments were not validated")
	}
}

func TestValidateKeyIDVersionDisagreement(t *testing.T) {
	h := newHarness(t, 3, 3)
	m := cloneManifest(h.pkg.Manifest)
	m.Segments[0].KeyVersion = 99
	out := Validate(m, h.pkg.License, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	if out.Segments[0].Reason != reason.KEY_ID_MISMATCH {
		t.Fatalf("reason = %s, want KEY_ID_MISMATCH", out.Segments[0].Reason)
	}
}

func TestValidateBadNonce(t *testing.T) {
	h := newHarness(t, 3, 3)
	m := cloneManifest(h.pkg.Manifest)
	m.Segments[0].Nonce = "not-base64"
	out := Validate(m, h.pkg.License, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	if out.Segments[0].Reason != reason.NONCE_INVALID {
		t.Fatalf("reason = %s, want NONCE_INVALID", out.Segments[0].Reason)
	}
}

func TestValidateDigestMismatch(t *testing.T) {
	h := newHarness(t, 3, 3)
	m := cloneManifest(h.pkg.Manifest)
	m.Segments[0].PlainDigest = strings.Repeat("00", 32)
	out := Validate(m, h.pkg.License, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	if out.Segments[0].Reason != reason.DIGEST_MISMATCH {
		t.Fatalf("reason = %s, want DIGEST_MISMATCH", out.Segments[0].Reason)
	}
}

func TestValidateWrongAssetLicense(t *testing.T) {
	h := newHarness(t, 3, 3)
	out := Validate(h.pkg.Manifest, h.pkg.License, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: "another-asset",
	})
	if out.License.Reason != reason.LICENSE_ASSET_MISMATCH {
		t.Fatalf("reason = %s, want LICENSE_ASSET_MISMATCH", out.License.Reason)
	}
}

func TestValidateTamperedLicenseSignature(t *testing.T) {
	h := newHarness(t, 3, 3)
	lic := *h.pkg.License
	lic.AssetID = "asset-test"
	lic.Serial = "lic-forged"
	out := Validate(h.pkg.Manifest, &lic, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	if out.License.Reason != reason.LICENSE_SIGNATURE {
		t.Fatalf("reason = %s, want LICENSE_SIGNATURE", out.License.Reason)
	}
}

func TestValidateReplayIsAtomic(t *testing.T) {
	h := newHarness(t, 3, 3)
	cache := replay.New()
	opts := ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID, Replay: cache,
	}
	first := Validate(h.pkg.Manifest, h.pkg.License, h.pkg.Ciphertexts, opts)
	if first.License.Reason != reason.NONE {
		t.Fatalf("first use: %+v", first.License)
	}
	second := Validate(h.pkg.Manifest, h.pkg.License, h.pkg.Ciphertexts, opts)
	if second.License.Reason != reason.LICENSE_REPLAY {
		t.Fatalf("second use: %s, want LICENSE_REPLAY", second.License.Reason)
	}
	for _, s := range second.Segments {
		if s.Reason != reason.LICENSE_REPLAY {
			t.Fatalf("segment %d reason = %s, want LICENSE_REPLAY", s.SegmentIndex, s.Reason)
		}
	}
}

func TestValidateKeyOutsideLicensedRange(t *testing.T) {
	h := newHarness(t, 9, 3)
	// Narrow the license scope to versions 1-2, so segments sealed under k3
	// are refused even though the key is present and unexpired.
	narrow, err := license.NewSigner(license.Options{
		Secret: h.pkg.Secret, Now: func() time.Time { return h.base },
	})
	if err != nil {
		t.Fatal(err)
	}
	lic, err := narrow.Issue(h.cfg.AssetID, 1, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	out := Validate(h.pkg.Manifest, lic, h.pkg.Ciphertexts, ValidateOptions{
		Secret: h.pkg.Secret, Now: h.base, Distributor: h.pkg.Distributor, AssetID: h.cfg.AssetID,
	})
	for _, s := range out.Segments {
		if s.SegmentIndex < 6 {
			if s.Reason != reason.NONE {
				t.Fatalf("segment %d unexpectedly failed: %s", s.SegmentIndex, s.Reason)
			}
			continue
		}
		if s.Reason != reason.KEY_OUT_OF_RANGE {
			t.Fatalf("segment %d reason = %s, want KEY_OUT_OF_RANGE", s.SegmentIndex, s.Reason)
		}
	}
}
