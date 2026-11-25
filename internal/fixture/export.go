package fixture

// HealthyMP4 returns the deterministic healthy sample MP4 container bytes.
func HealthyMP4() []byte { return append([]byte(nil), sampleMP4()...) }

// HealthyMXF returns the deterministic healthy sample MXF container bytes.
func HealthyMXF() []byte { return append([]byte(nil), sampleMXF()...) }
