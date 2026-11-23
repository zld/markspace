// Package samplegen produces the deterministic in-memory container fixtures
// used by the tests and the demo: a supported MP4 subset (written with
// go-mp4), length-delimited MXF KLV streams and synthetic RTP packet traces.
// No external files or network access are involved.
package samplegen

// MP4TrackSpec describes one synthetic MP4 track.
type MP4TrackSpec struct {
	TrackID    uint32
	Timescale  uint32
	Width      uint32
	Height     uint32
	SampleN    uint32 // number of samples in stts/stsz/stco
	SampleDur  uint32 // constant stts delta per sample
	Rotation90 int    // 0,1,2,3 -> 0/90/180/270 degrees clockwise
	ColourType [4]byte
	Primaries  uint16
	Transfer   uint16
	Matrix     uint16
	FullRange  bool
	// Crop, when Width>0, is emitted as a custom "crp0" box (4 big-endian
	// uint32: x,y,width,height) inside stbl.
	CropX, CropY, CropW, CropH uint32
}

// MP4Spec is a complete synthetic MP4 file.
type MP4Spec struct {
	Tracks []MP4TrackSpec
	// BadSampleRange, when true, writes an stco offset past the mdat payload
	// so the parser must reject the track as a recoverable corruption.
	BadSampleRange bool
	// Truncate cuts the produced bytes at TruncateAt (0 = no truncation).
	Truncate bool
	// TruncateAt is the cut position in bytes; defaults to 60% of length.
	TruncateAt int
}

// MXFTag is one local-set item of an MXF fixture: tag -> raw little-endian
// value payload (already encoded).
type MXFTag struct {
	Tag   uint16
	Value []byte
}

// MXFTrackSpec describes one synthetic MXF track descriptor.
type MXFTrackSpec struct {
	TrackID                    uint32
	EditRateN                  uint32 // edit rate numerator (ticks per second)
	EditRateD                  uint32 // edit rate denominator
	Width                      uint32
	Height                     uint32
	Rotation90                 int
	Primaries                  uint16
	Transfer                   uint16
	Matrix                     uint16
	FullRange                  bool
	CropX, CropY, CropW, CropH uint32
}

// MXFSpec is a complete synthetic MXF KLV byte stream.
type MXFSpec struct {
	Tracks   []MXFTrackSpec
	Truncate bool // emit a truncated final KLV item
}
