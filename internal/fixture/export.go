package fixture

// MXF builds the minimal MXF KLV image for the given track specs and returns
// the bytes together with the ground-truth sample table.
func MXF(tracks []MXFTrackSpec) ([]byte, []MXFSample, error) {
	return buildMXF(tracks)
}
