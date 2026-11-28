// Package fixture generates the deterministic in-memory MP4 and MXF sample
// data used by tests and the demo command. No external files are referenced:
// every byte is produced from a FixtureSpec so reruns with the same seed are
// byte-identical.
package fixture

// ColorSpec describes one retained color description.
type ColorSpec struct {
	Type      string
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
}

// CropSpec mirrors the clap rational fields; zero-value means "no crop".
type CropSpec struct {
	CleanWidthN, CleanWidthD   uint32
	CleanHeightN, CleanHeightD uint32
	HorizOffN, HorizOffD       int32
	VertOffN, VertOffD         int32
}

// VideoSpec configures a synthetic video track.
type VideoSpec struct {
	TrackID   uint32
	Timescale uint32
	Width     uint16
	Height    uint16
	Rotation  int // 0, 90, 180, 270
	Crop      *CropSpec
	Colors    []ColorSpec
	// Delta is the per-sample delta; Frames is the sample count.
	Delta  uint32
	Frames uint32
}

// AudioSpec configures a synthetic audio track.
type AudioSpec struct {
	TrackID    uint32
	Timescale  uint32
	Channels   uint16
	SampleRate uint32
	SampleSize uint16
	Delta      uint32
	Samples    uint32
}

// Spec is one synthetic container: one video plus zero or more audio tracks.
type Spec struct {
	Name  string
	Video VideoSpec
	Audio []AudioSpec
}

// RotationMatrix maps the demo rotation values onto tkhd 16.16 matrices.
func RotationMatrix(deg int) [9]int32 {
	const one = 0x00010000
	switch deg {
	case 90:
		// Clockwise 90 degrees.
		return [9]int32{0, one, 0, -one, 0, 0, 0, 0, one}
	case 180:
		return [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, one}
	case 270:
		return [9]int32{0, -one, 0, one, 0, 0, 0, 0, one}
	default:
		return [9]int32{one, 0, 0, 0, one, 0, 0, 0, one}
	}
}
