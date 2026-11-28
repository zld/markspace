package model

// Hard safety limits. The kernel only processes bounded local input and must
// reject any length that escapes these bounds instead of growing without limit.
const (
	// MaxFramePayload caps a single frame payload at 1 MiB.
	MaxFramePayload = 1 << 20
	// MaxTotalInput caps the whole accepted input byte stream at 256 MiB.
	MaxTotalInput = 256 << 20
	// MaxRecordHeader is the fixed on-wire record header size (see ingest).
	MaxRecordHeader = 64
)

// LengthError builds a KindLength CodeError anchored at the given offset.
func LengthError(off RawOffset, msg string) *CodeError {
	return &CodeError{
		Kind:     KindLength,
		Offset:   off,
		FirstPos: off.Start,
		Message:  msg,
	}
}

// ChecksumError builds a KindChecksum CodeError whose FirstPos is the first
// byte of the offending payload.
func ChecksumError(off RawOffset, track, source, seq uint32) *CodeError {
	return &CodeError{
		Kind:     KindChecksum,
		Track:    track,
		Source:   source,
		Seq:      seq,
		Offset:   off,
		FirstPos: off.Start,
		Message:  "crc32 mismatch",
	}
}
