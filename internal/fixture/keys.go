package fixture

// This file exposes the subset's MXF label constants to the ingest parser
// without making every writer constant public.

// PartitionKey returns the fixed Header Partition Pack 16-byte label.
func PartitionKey() [16]byte { return partitionKey }

// IDTrackStart is the item id that opens a new track group.
func IDTrackStart() byte { return mxfIDTrackStart }

// Item IDs used by the MXF subset.
const (
	IDKind       = mxfIDKind
	IDTimescale  = mxfIDTimescale
	IDDuration   = mxfIDDuration
	IDWidth      = mxfIDWidth
	IDHeight     = mxfIDHeight
	IDRotation   = mxfIDRotation
	IDCrop       = mxfIDCrop
	IDColor      = mxfIDColor
	IDChannels   = mxfIDChannels
	IDRate       = mxfIDRate
	IDSampleSize = mxfIDSampleSize
	IDSample     = mxfIDSample
)
