// Package reason defines the stable machine-readable result codes emitted by
// the media packaging and license validation pipeline.
//
// Codes are part of the CLI contract: build scripts match on them, so existing
// values must never be renamed. Adding a new code is a compatible change.
package reason

// Code is a stable failure classification for a single segment or for the
// whole validation run. The empty-like success value is NONE.
type Code string

const (
	// NONE marks a successful operation.
	NONE Code = "NONE"

	// Argument and build failures (exit code 1).
	BUILD_ERROR   Code = "BUILD_ERROR"
	INVALID_INPUT Code = "INVALID_INPUT"

	// DuplicateNonce means the random source produced a nonce that is already
	// present in the manifest. Packaging aborts so that no existing segment is
	// overwritten and no nonce is ever reused under the same key.
	DuplicateNonce Code = "DUPLICATE_NONCE"

	// License failures, reported before any segment is decrypted.
	LICENSE_INVALID        Code = "LICENSE_INVALID"
	LICENSE_SIGNATURE      Code = "LICENSE_SIGNATURE"
	LICENSE_MISSING_FIELD  Code = "LICENSE_MISSING_FIELD"
	LICENSE_ASSET_MISMATCH Code = "LICENSE_ASSET_MISMATCH"
	LICENSE_EXPIRED        Code = "LICENSE_EXPIRED"
	LICENSE_REPLAY         Code = "LICENSE_REPLAY"

	// Key resolution failures.
	KEY_UNKNOWN      Code = "KEY_UNKNOWN"
	KEY_EXPIRED      Code = "KEY_EXPIRED"
	KEY_OUT_OF_RANGE Code = "KEY_OUT_OF_RANGE"
	KEY_ID_MISMATCH  Code = "KEY_ID_MISMATCH"

	// Manifest and segment failures.
	SEGMENT_MISSING        Code = "SEGMENT_MISSING"
	SEGMENT_DUPLICATE      Code = "SEGMENT_DUPLICATE"
	SEGMENT_ORDER          Code = "SEGMENT_ORDER"
	NONCE_INVALID          Code = "NONCE_INVALID"
	DIGEST_MISMATCH        Code = "DIGEST_MISMATCH"
	SEGMENT_COUNT_MISMATCH Code = "SEGMENT_COUNT_MISMATCH"
	AUTH_FAILED            Code = "AUTH_FAILED"
)

// IsFailure reports whether c is anything other than success.
func (c Code) IsFailure() bool { return c != NONE && c != "" }
