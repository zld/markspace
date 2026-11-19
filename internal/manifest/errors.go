// Package manifest defines the on-disk and in-memory data model for media
// assets, encrypted chunk envelopes, licenses and the stable error codes used
// across every module.
package manifest

// FailureReason is a stable, machine-readable error category. Verification
// failures only ever report these categories; key material and plaintext are
// never included in reports.
type FailureReason string

const (
	ReasonNone FailureReason = ""

	// ReasonAuthFailed: AES-GCM authentication failed (tampered ciphertext
	// or additional data, truncation, wrong key).
	ReasonAuthFailed FailureReason = "auth_failed"
	// ReasonChunkReordered: chunk index out of sequence / wrong-order chunk.
	ReasonChunkReordered FailureReason = "chunk_reordered"
	// ReasonChunkDuplicate: the same chunk index was processed twice.
	ReasonChunkDuplicate FailureReason = "chunk_duplicate"
	// ReasonTruncated: envelope is structurally shorter than required.
	ReasonTruncated FailureReason = "truncated"
	// ReasonLicenseSignature: license HMAC signature does not verify.
	ReasonLicenseSignature FailureReason = "license_signature_invalid"
	// ReasonAssetMismatch: license is bound to a different asset ID.
	ReasonAssetMismatch FailureReason = "asset_mismatch"
	// ReasonRangeExceeded: chunk/round is outside the licensed scope.
	ReasonRangeExceeded FailureReason = "range_exceeded"
	// ReasonNotYetValid: license NotBefore is in the future.
	ReasonNotYetValid FailureReason = "not_yet_valid"
	// ReasonExpired: license or key round is past its validity window.
	ReasonExpired FailureReason = "expired"
	// ReasonKeyExpired: old key round used outside its overlap window.
	ReasonKeyExpired FailureReason = "key_expired"
	// ReasonReplay: nonce has already been accepted.
	ReasonReplay FailureReason = "replay"
	// ReasonOutOfOrder: request sequence number moved backwards.
	ReasonOutOfOrder FailureReason = "sequence_out_of_order"
	// ReasonWindowOutside: request is older than the replay cache window.
	ReasonWindowOutside FailureReason = "window_outside"
	// ReasonUnknownRound: dispatcher was asked for a non-existent round.
	ReasonUnknownRound FailureReason = "unknown_round"
	// ReasonDispatcherClosed: key requested after the dispatcher closed.
	ReasonDispatcherClosed FailureReason = "dispatcher_closed"
	// ReasonRevokedRound: key round has been explicitly revoked.
	ReasonRevokedRound FailureReason = "revoked_round"
	// ReasonLimitExceeded: a configured safety limit was exceeded.
	ReasonLimitExceeded FailureReason = "limit_exceeded"
	// ReasonInvalidInput: malformed argument or manifest data.
	ReasonInvalidInput FailureReason = "invalid_input"
)

// CodedError pairs a stable failure reason with a human-readable message.
// Every module returns CodedError values so the pipeline can record the
// category without inspecting free-form strings.
type CodedError struct {
	Code   FailureReason
	Detail string
}

func (e *CodedError) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Detail
}

// NewError builds a CodedError.
func NewError(code FailureReason, detail string) *CodedError {
	return &CodedError{Code: code, Detail: detail}
}

// ErrorCode extracts the stable reason from any error, returning
// ReasonInvalidInput for errors that do not carry a code.
func ErrorCode(err error) FailureReason {
	if err == nil {
		return ReasonNone
	}
	if ce, ok := err.(*CodedError); ok {
		return ce.Code
	}
	return ReasonInvalidInput
}

// Per-chunk processing statuses reported in JSON output.
const (
	StatusOK     = "ok"
	StatusFailed = "failed"
)
