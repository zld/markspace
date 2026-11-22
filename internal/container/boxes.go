package container

import (
	"encoding/binary"

	"mq/internal/timeline"
)

// Cursor 为带边界检查的字节游标：所有越界访问在此统一报错并携带偏移。
type Cursor struct {
	buf []byte
	off int64
}

// NewCursor 创建游标。
func NewCursor(buf []byte) *Cursor { return &Cursor{buf: buf} }

// Offset 返回当前偏移。
func (c *Cursor) Offset() int64 { return c.off }

// Remaining 返回剩余字节数。
func (c *Cursor) Remaining() int64 { return int64(len(c.buf)) - c.off }

// Done 报告是否已读尽。
func (c *Cursor) Done() bool { return c.Remaining() <= 0 }

// Need 校验剩余长度，不足时报 parse 阶段截断错误。
func (c *Cursor) Need(n int64, track, what string) error {
	if n < 0 || c.Remaining() < n {
		return timeline.NewErr(timeline.StageParse, track, c.off,
			"truncated %s: need %d bytes, have %d", what, n, c.Remaining())
	}
	return nil
}

// Read 读取 n 字节并前进。
func (c *Cursor) Read(n int64, track, what string) ([]byte, error) {
	if err := c.Need(n, track, what); err != nil {
		return nil, err
	}
	b := c.buf[c.off : c.off+n]
	c.off += n
	return b, nil
}

// Skip 跳过 n 字节。
func (c *Cursor) Skip(n int64, track, what string) error {
	if err := c.Need(n, track, what); err != nil {
		return err
	}
	c.off += n
	return nil
}

// U8 读取单字节。
func (c *Cursor) U8(track, what string) (uint8, error) {
	b, err := c.Read(1, track, what)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// U16 读取大端 16 位整数。
func (c *Cursor) U16(track, what string) (uint16, error) {
	b, err := c.Read(2, track, what)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

// U32 读取大端 32 位整数。
func (c *Cursor) U32(track, what string) (uint32, error) {
	b, err := c.Read(4, track, what)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint32(b), nil
}

// U64 读取大端 64 位整数。
func (c *Cursor) U64(track, what string) (uint64, error) {
	b, err := c.Read(8, track, what)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(b), nil
}

// BoxHeader 描述一个 MP4 盒的位置与边界。
type BoxHeader struct {
	Type    string
	Offset  int64 // 盒头起始偏移
	Size    int64 // 含头部总大小
	Payload int64 // 载荷起始偏移
}

// PayloadLen 返回载荷长度。
func (h BoxHeader) PayloadLen() int64 { return h.Offset + h.Size - h.Payload }

// ReadBoxHeader 读取并校验盒头；非法长度与截断盒在此报错并携带盒偏移。
func (c *Cursor) ReadBoxHeader(track string) (BoxHeader, error) {
	h := BoxHeader{Offset: c.off}
	if err := c.Need(8, track, "box header"); err != nil {
		return h, err
	}
	size32, _ := c.U32(track, "box size")
	typ, _ := c.Read(4, track, "box type")
	h.Type = string(typ)
	size := int64(size32)
	header := int64(8)
	switch size32 {
	case 0: // 延伸至文件尾
		size = c.Remaining() + header
	case 1: // 64 位扩展长度
		s64, err := c.U64(track, "box largesize")
		if err != nil {
			return h, err
		}
		if s64 > uint64(1<<62) {
			return h, timeline.NewErr(timeline.StageParse, track, h.Offset,
				"illegal box largesize %d for %q", s64, h.Type)
		}
		size = int64(s64)
		header = 16
	}
	if size < header {
		return h, timeline.NewErr(timeline.StageParse, track, h.Offset,
			"illegal box size %d for %q", size, h.Type)
	}
	if size-header > c.Remaining() {
		return h, timeline.NewErr(timeline.StageParse, track, h.Offset,
			"truncated box %q: size %d exceeds remaining %d", h.Type, size, c.Remaining()+header)
	}
	h.Size = size
	h.Payload = h.Offset + header
	return h, nil
}

// CheckSpan 校验 [off, off+length) 是否落在 [0, total) 内。
func CheckSpan(total, off, length int64) bool {
	return off >= 0 && length >= 0 && off+length <= total
}
