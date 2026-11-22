// Package recovery persists pipeline progress so a restarted node resumes
// instead of redoing completed work.
//
// A Store is bound to one checkpoint file. Begin validates the existing file
// against the current input digest and parameters digest: a mismatch is
// refused (ErrDigestMismatch), and a payload whose bytes no longer hash to its
// recorded digest is refused as corrupt (ErrCorruptCheckpoint). Only when both
// digests match are committed stages reused.
//
// Writes are atomic — a temporary file in the same directory, fsynced and
// renamed — so a crash can leave either the old journal or the new one, never
// a half-written one. Re-committing a stage replaces its record, which makes a
// retried stage idempotent.
package recovery
