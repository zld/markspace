package fixture

import (
	"fmt"

	"continuity/internal/media"
)

// Defect is the pre-embedded expected failure location for a corrupt sample.
type Defect struct {
	Kind        string `json:"kind"`
	File        string `json:"file,omitempty"`
	Offset      int64  `json:"embedded_offset"`
	Description string `json:"description"`
}

// CorruptSample bundles bytes with the defect planted in them.
type CorruptSample struct {
	Name   string
	Data   []byte
	Defect Defect
}

// TruncatedVINTSample plants an 8-byte size VINT whose continuation is absent.
func TruncatedVINTSample() CorruptSample {
	id := IDBytes(media.IDEBMLEBML)
	offset := int64(len(id)) // size VINT starts immediately after the four ID bytes
	data := append(append([]byte(nil), id...), 0x01, 0xff, 0xff)
	return CorruptSample{
		Name: "truncated-vint.mkv",
		Data: data,
		Defect: Defect{
			Kind:        "truncated_vint",
			Offset:      offset,
			Description: "first size byte declares an eight-byte VINT, but only two continuation bytes exist",
		},
	}
}

// OutOfBoundsBlockSample plants a SimpleBlock whose declared size passes the
// end of its legal Cluster parent.
func OutOfBoundsBlockSample() CorruptSample {
	badPayloadDeclared := uint64(160)
	badHeader := append(IDBytes(media.IDSimpleBlock), VINTSize(badPayloadDeclared)...)
	badActual := append(append([]byte(nil), badHeader...), make([]byte, 6)...)
	timecode := UintElement(media.IDTimecode, 0)
	clusterPayload := append(append([]byte(nil), timecode...), badActual...)
	cluster := Element(media.IDCluster, clusterPayload)
	segment := Element(media.IDSegment, cluster)
	header := buildEBMLHeader()

	offset := int64(len(header))
	offset += int64(len(IDBytes(media.IDSegment)) + len(VINTSize(uint64(len(cluster)))))
	offset += int64(len(IDBytes(media.IDCluster)) + len(VINTSize(uint64(len(clusterPayload)))))
	offset += int64(len(timecode))

	data := append(append([]byte(nil), header...), segment...)
	return CorruptSample{
		Name: "out-of-bounds-block.mkv",
		Data: data,
		Defect: Defect{
			Kind:        "out_of_bounds_block",
			Offset:      offset,
			Description: fmt.Sprintf("SimpleBlock declares %d payload bytes but only 6 are present inside Cluster", badPayloadDeclared),
		},
	}
}

// IllegalBoundarySample places an oversized block at the legal end of a
// known-length Segment containing an unknown-length Cluster.
func IllegalBoundarySample() CorruptSample {
	validBlock := SimpleBlockElement(1, 0, true, make([]byte, 12))
	badHeader := append(IDBytes(media.IDSimpleBlock), VINTSize(96)...)
	badActual := append(append([]byte(nil), badHeader...), 0x10)
	timecode := UintElement(media.IDTimecode, 0)
	clusterPayload := append(append(append([]byte(nil), timecode...), validBlock...), badActual...)
	cluster := UnknownElement(media.IDCluster, clusterPayload, 8)
	segment := Element(media.IDSegment, cluster)
	header := buildEBMLHeader()

	offset := int64(len(header))
	offset += int64(len(IDBytes(media.IDSegment)) + len(VINTSize(uint64(len(cluster)))))
	offset += int64(len(IDBytes(media.IDCluster)) + len(UnknownVINT(8)))
	offset += int64(len(timecode) + len(validBlock))

	data := append(append([]byte(nil), header...), segment...)
	return CorruptSample{
		Name: "illegal-boundary.mkv",
		Data: data,
		Defect: Defect{
			Kind:        "illegal_boundary",
			Offset:      offset,
			Description: "unknown-length Cluster ends at the legal Segment boundary; trailing block cannot cross it",
		},
	}
}
