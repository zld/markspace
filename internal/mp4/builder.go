package mp4

import "encoding/binary"

type byteBuilder []byte

func (b *byteBuilder) u8(v byte) { *b = append(*b, v) }
func (b *byteBuilder) u16(v uint16) {
	var x [2]byte
	binary.BigEndian.PutUint16(x[:], v)
	*b = append(*b, x[:]...)
}
func (b *byteBuilder) u32(v uint32) {
	var x [4]byte
	binary.BigEndian.PutUint32(x[:], v)
	*b = append(*b, x[:]...)
}
func (b *byteBuilder) u64(v uint64) {
	var x [8]byte
	binary.BigEndian.PutUint64(x[:], v)
	*b = append(*b, x[:]...)
}
func (b *byteBuilder) raw(p []byte)    { *b = append(*b, p...) }
func (b *byteBuilder) fourcc(s string) { var x [4]byte; copy(x[:], s); *b = append(*b, x[:]...) }
func (b *byteBuilder) begin(t string) int {
	start := len(*b)
	b.u32(0)
	b.fourcc(t)
	return start
}
func (b *byteBuilder) end(start int) {
	binary.BigEndian.PutUint32((*b)[start:start+4], uint32(len(*b)-start))
}
func (b *byteBuilder) fullBox(version byte, flags uint32) {
	b.u8(version)
	b.u8(byte(flags >> 16))
	b.u8(byte(flags >> 8))
	b.u8(byte(flags))
}
