// Package diagnose defines how every stage reports a fault and how those
// faults become the half-open damaged ranges in the final report.
//
// An Issue is the unit of reporting: category, stage, format, asset, span and
// a human-readable detail. The span is always in bytes of the original input,
// never in normalized coordinates, so a retry can re-read exactly the bytes
// that produced the finding.
//
// Ranges merges issues into the smallest set of non-overlapping intervals that
// covers the damage. Merging never loses information: each range keeps the
// sorted list of contributing categories and stages and the number of merged
// issues. Benign events such as an RTP rollover do not create a range on their
// own, but they are folded in when they fall inside real damage.
package diagnose
