package fixture

import (
	"bytes"

	"mediacheck/ebml"
	"mediacheck/timeline"
)

// Corruption identifies an injectable fault for the demo and tests.
type Corruption string

const (
	// CorruptTruncateCluster cuts the stream mid-way through a later cluster,
	// leaving every earlier cluster intact and indexable.
	CorruptTruncateCluster Corruption = "cluster"
	// CorruptCRC flips payload bytes so a cluster CRC verification fails.
	CorruptCRC Corruption = "crc"
)

// TruncateAtCluster finds the offset of the nth Cluster element (0-based) and
// returns the stream cut inside that cluster, after its timecode element.
func TruncateAtCluster(stream []byte, nth int) ([]byte, int64, error) {
	r := ebml.NewReader(bytes.NewReader(stream))
	seen := 0
	for {
		h, err := r.Next()
		if err != nil {
			return nil, 0, timeline.New(timeline.CatCorrupt, "no cluster found to truncate").Wrapf(err)
		}
		if h.ID != ebml.IDSegment {
			if err := r.Skip(h); err != nil {
				return nil, 0, err
			}
			continue
		}
		if err := r.Enter(h); err != nil {
			return nil, 0, err
		}
		break
	}
	for {
		h, err := r.Next()
		if err != nil {
			return nil, 0, timeline.New(timeline.CatCorrupt, "cluster not found").Wrapf(err)
		}
		if h.ID != ebml.IDCluster {
			if err := r.Skip(h); err != nil {
				return nil, 0, err
			}
			continue
		}
		if seen == nth {
			// Cut a few payload bytes into the cluster so its header parses
			// but a child element is truncated.
			cut := h.DataOffset + 4
			if cut >= h.End() {
				cut = h.DataOffset + 1
			}
			out := make([]byte, cut)
			copy(out, stream[:cut])
			return out, cut, nil
		}
		seen++
		if err := r.Skip(h); err != nil {
			return nil, 0, err
		}
	}
}

// FlipClusterPayload flips one byte inside the nth cluster payload so its
// stored CRC no longer matches while element framing stays intact.
func FlipClusterPayload(stream []byte, nth int) ([]byte, int64, error) {
	r := ebml.NewReader(bytes.NewReader(stream))
	for {
		h, err := r.Next()
		if err != nil {
			return nil, 0, timeline.New(timeline.CatCorrupt, "segment not found").Wrapf(err)
		}
		if h.ID == ebml.IDSegment {
			if err := r.Enter(h); err != nil {
				return nil, 0, err
			}
			break
		}
		if err := r.Skip(h); err != nil {
			return nil, 0, err
		}
	}
	seen := 0
	for {
		h, err := r.Next()
		if err != nil {
			return nil, 0, timeline.New(timeline.CatCorrupt, "cluster not found").Wrapf(err)
		}
		if h.ID != ebml.IDCluster {
			if err := r.Skip(h); err != nil {
				return nil, 0, err
			}
			continue
		}
		if seen == nth {
			out := append([]byte(nil), stream...)
			pos := h.DataOffset + (h.Size / 2)
			out[pos] ^= 0xFF
			return out, pos, nil
		}
		seen++
		if err := r.Skip(h); err != nil {
			return nil, 0, err
		}
	}
}
