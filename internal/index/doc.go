// Package index derives keyframe intervals from a parsed sample table.
//
// The gap between two keyframes is the sum of the real stts sample durations
// between them. It is never derived from an average frame rate: on VFR input
// consecutive gaps legitimately differ, and the index reports the minimum,
// maximum and mean as distinct values along with a VFR flag.
//
// The index also carries evidence about trust. A keyframe whose byte span
// intersects a known damaged range is marked untrusted, so a downstream retry
// can start from the nearest intact keyframe instead of seeking to a frame
// whose bytes are missing.
package index
