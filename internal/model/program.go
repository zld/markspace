// Package model defines the shared data model: programs, elementary
// streams and versioned PID-mapping snapshots. A snapshot is the only
// mapping the slicer ever uses, so packets before and after a table
// update can never be mixed into the same program map.
package model

import "sort"

// Stream describes one elementary stream inside a program.
type Stream struct {
	Program    uint16 // program number the stream belongs to
	PID        uint16 // elementary stream PID
	StreamType uint8  // PMT stream_type
}

// Snapshot is a complete PID mapping published at a packet boundary.
// It stays in effect from EffectivePacket (inclusive) until the next
// snapshot becomes effective.
type Snapshot struct {
	Index           int             // 0-based interval index
	EffectivePacket int             // TS packet index where the mapping takes effect
	PATVersion      int             // published PAT version at this boundary
	PMTVersions     map[uint16]int  // program number -> published PMT version
	Streams         []Stream        // sorted by PID
	PIDs            map[uint16]bool // fast membership test, mirrors Streams
}

// NewSnapshot builds a snapshot from a stream list, sorting streams by
// PID for deterministic output.
func NewSnapshot(index, effective, patVersion int, pmtVersions map[uint16]int, streams []Stream) Snapshot {
	sorted := make([]Stream, len(streams))
	copy(sorted, streams)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].PID < sorted[j].PID })
	pids := make(map[uint16]bool, len(sorted))
	for _, s := range sorted {
		pids[s.PID] = true
	}
	versions := make(map[uint16]int, len(pmtVersions))
	for k, v := range pmtVersions {
		versions[k] = v
	}
	return Snapshot{
		Index:           index,
		EffectivePacket: effective,
		PATVersion:      patVersion,
		PMTVersions:     versions,
		Streams:         sorted,
		PIDs:            pids,
	}
}

// SameMapping reports whether two snapshots map the same set of
// (program, PID, stream type) entries. Version-only table updates do
// not open a new mapping interval.
func SameMapping(a, b []Stream) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
