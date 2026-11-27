// Package fault defines stable error codes and byte offsets shared by every
// mediacore stage. A Fault is safe to serialize into a manifest report.
package fault

// Code is a stable, machine-readable error identifier. These strings are part
// of the command line contract and must not change between runs.
type Code string

const (
	// Bitstream scanning and keyframe indexes.
	CodeScanIndexOutOfRange  Code = "MC_SCAN_INDEX_OUT_OF_RANGE"
	CodeScanIndexNotBoundary Code = "MC_SCAN_INDEX_NOT_BOUNDARY"
	CodeScanIndexNotKeyframe Code = "MC_SCAN_INDEX_NOT_KEYFRAME"
	CodeScanIndexTimeReorder Code = "MC_SCAN_INDEX_TIME_REORDER"
	CodeScanTruncatedHeader  Code = "MC_SCAN_TRUNCATED_HEADER"
	CodeScanIllegalBoundary  Code = "MC_SCAN_ILLEGAL_BOUNDARY"
	CodeScanUnsupportedCodec Code = "MC_SCAN_UNSUPPORTED_CODEC"
	CodeScanNoFrames         Code = "MC_SCAN_NO_FRAMES"
	CodeScanMissingParameter Code = "MC_SCAN_MISSING_SEQUENCE_HEADER"
	CodeScanIndexVersion     Code = "MC_SCAN_INDEX_VERSION"
	CodeScanIndexCodec       Code = "MC_SCAN_INDEX_CODEC"

	// RTP jitter buffering and recovery.
	CodeRTPMalformed      Code = "MC_RTP_MALFORMED_PACKET"
	CodeRTPGapUnrecovered Code = "MC_RTP_GAP_UNRECOVERABLE"
	CodeRTPBufferLimit    Code = "MC_RTP_BUFFER_LIMIT"
	CodeRTPSequenceLimit  Code = "MC_RTP_SEQUENCE_OUT_OF_RANGE"

	// Audio/video synchronisation.
	CodeSyncMonotonicity Code = "MC_SYNC_MONOTONICITY"
	CodeSyncLateFrame    Code = "MC_SYNC_LATE_FRAME"
	CodeSyncEmptyTrack   Code = "MC_SYNC_EMPTY_TRACK"

	// Quality and processing budget.
	CodeQualityBelowMin Code = "MC_QUALITY_BELOW_MIN"
	CodeBudgetExceeded  Code = "MC_BUDGET_EXCEEDED"
	CodeNoCandidate     Code = "MC_QUALITY_NO_CANDIDATE"
	CodeRateDeviation   Code = "MC_QUALITY_RATE_DEVIATION"

	// Manifest adaption and migration.
	CodeManifestVersion      Code = "MC_MANIFEST_VERSION_UNSUPPORTED"
	CodeManifestMalformed    Code = "MC_MANIFEST_MALFORMED"
	CodeManifestEmpty        Code = "MC_MANIFEST_EMPTY"
	CodeManifestCount        Code = "MC_MANIFEST_ENTRY_COUNT"
	CodeManifestAlignment    Code = "MC_MANIFEST_TIME_ALIGNMENT"
	CodeManifestDuplicateID  Code = "MC_MANIFEST_DUPLICATE_ID"
	CodeMigrationStage       Code = "MC_MIGRATION_STAGE"
	CodeMigrationInterrupted Code = "MC_MIGRATION_INTERRUPTED"
	CodeMigrationRollback    Code = "MC_MIGRATION_ROLLBACK"

	// Archive.
	CodeArchiveHashMismatch Code = "MC_ARCHIVE_HASH_MISMATCH"
	CodeArchiveUnverified   Code = "MC_ARCHIVE_UNVERIFIED"

	// Local tooling.
	CodeFFmpegUnavailable Code = "MC_FFMPEG_UNAVAILABLE"
	CodeFFmpegFailed      Code = "MC_FFMPEG_FAILED"
	CodeInternal          Code = "MC_INTERNAL"
)
