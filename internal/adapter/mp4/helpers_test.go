package mp4_test

import "encoding/binary"

func binaryU32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

func putU32(b []byte, v uint32) { binary.BigEndian.PutUint32(b, v) }
