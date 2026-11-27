package fixture

// MXF builds the minimal MXF KLV image for the given track specs and returns
// the bytes together with the ground-truth sample table.
func MXF(tracks []MXFTrackSpec) ([]byte, []MXFSample, error) {
	return buildMXF(tracks)
}

// MP4 builds the minimal ISO-BMFF image for the given track specs and returns
// the bytes together with the ground-truth sample table.
func MP4(tracks []MP4TrackSpec) ([]byte, []MP4Sample, error) {
	return buildMP4(tracks)
}
