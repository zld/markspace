// Package ebml 提供 Matroska/WebM 的流式 EBML 解析。
// 支持未知长度 Segment/Cluster、损坏块隔离与共享错误模型。
package ebml

import (
	"fmt"
	"io"

	ebmlgo "github.com/at-wat/ebml-go"
)

// Code 为跨模块共享的错误码。
type Code string

const (
	CodeTruncatedElement Code = "EBML_TRUNCATED_ELEMENT"
	CodeCorruptBlock     Code = "EBML_CORRUPT_BLOCK"
	CodeMissingTimestamp Code = "EBML_MISSING_TIMESTAMP"
	CodeTimestampWrap    Code = "EBML_TIMESTAMP_WRAP"
	CodeUnsupported      Code = "EBML_UNSUPPORTED"
	CodeBadHeader        Code = "EBML_BAD_HEADER"
)

// Error 为共享错误模型：错误码 + 字节偏移 + 描述。
type Error struct {
	Code   Code   `json:"code"`
	Offset int64  `json:"offset"`
	Msg    string `json:"msg"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s@%d: %s", e.Code, e.Offset, e.Msg) }

// 元素 ID 常量。
const (
	IDEBML           uint32 = 0x1A45DFA3
	IDSegment        uint32 = 0x18538067
	IDInfo           uint32 = 0x1549A966
	IDTimestampScale uint32 = 0x2AD7B1
	IDTracks         uint32 = 0x1654AE6B
	IDCluster        uint32 = 0x1F43B675
	IDTimestamp      uint32 = 0xE7
	IDSimpleBlock    uint32 = 0xA3
	IDBlockGroup     uint32 = 0xA0
	IDBlock          uint32 = 0xA1
	IDCues           uint32 = 0x1C53BB6B
)

// Element 为一个已读出的元素头。
type Element struct {
	ID         uint32
	Size       int64 // -1 表示未知长度
	Offset     int64 // 元素头起始偏移
	DataOffset int64 // 载荷起始偏移
}

// Block 为解析出的媒体块；AbsTS 由 clock 模块统一填充（纳秒）。
type Block struct {
	Track       uint64
	ClusterTS   int64 // 所在簇时间戳（刻度单位）；HasClusterTS=false 时无效
	HasClusTS   bool
	RelTS       int16
	Data        []byte
	Offset      int64
	AbsTS       int64
}

// Sink 接收流式解析事件。
type Sink interface {
	OnInfo(timestampScale uint64)
	OnTracks(tracks []Track)
	OnBlock(b Block)
	OnError(err *Error)
}

type countingReader struct {
	r   io.Reader
	pos int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.pos += int64(n)
	return n, err
}

// readVINT 读取 EBML 变长整数；keepMarker 用于元素 ID。返回（值， 宽度， 错误）。
func readVINT(c *countingReader, keepMarker bool) (uint64, int, error) {
	var b [1]byte
	if _, err := io.ReadFull(c, b[:]); err != nil {
		return 0, 0, err
	}
	b0 := b[0]
	if b0 == 0 {
		return 0, 0, fmt.Errorf("invalid vint leading byte at %d", c.pos-1)
	}
	width := 1
	for mask := byte(0x80); mask != 0 && b0&mask == 0; mask >>= 1 {
		width++
	}
	if width > 8 {
		return 0, 0, fmt.Errorf("vint too wide at %d", c.pos-1)
	}
	val := uint64(b0)
	if !keepMarker {
		val = uint64(b0) & (uint64(0xFF) >> uint(width))
	}
	for i := 1; i < width; i++ {
		if _, err := io.ReadFull(c, b[:]); err != nil {
			return 0, 0, err
		}
		val = val<<8 | uint64(b[0])
	}
	return val, width, nil
}

// Parser 为流式 EBML 解析器。
type Parser struct {
	c       *countingReader
	fileEnd int64 // 文件总长度，用于截断检测；-1 未知
	pending *Element
}

// NewParser 构造解析器；fileEnd 为输入总长，用于检测截断元素。
func NewParser(r io.Reader, fileEnd int64) *Parser {
	return &Parser{c: &countingReader{r: r}, fileEnd: fileEnd}
}

func (p *Parser) readHeader() (*Element, error) {
	if p.pending != nil {
		e := p.pending
		p.pending = nil
		return e, nil
	}
	off := p.c.pos
	id, _, err := readVINT(p.c, true)
	if err != nil {
		return nil, err
	}
	sz, sw, err := readVINT(p.c, false)
	if err != nil {
		return nil, err
	}
	e := &Element{ID: uint32(id), Size: int64(sz), Offset: off, DataOffset: p.c.pos}
	if sz == (uint64(1)<<uint(7*sw))-1 { // 全 1：未知长度
		e.Size = -1
	}
	return e, nil
}

func (p *Parser) peekHeader() (*Element, error) {
	e, err := p.readHeader()
	if err != nil {
		return nil, err
	}
	p.pending = e
	return e, nil
}

func (p *Parser) skip(n int64) error {
	_, err := io.CopyN(io.Discard, p.c, n)
	return err
}

func (p *Parser) readBytes(n int64) ([]byte, error) {
	if n < 0 || n > 1<<28 { // 拒绝超出配置块长的数据
		return nil, fmt.Errorf("refuse element payload of %d bytes at %d", n, p.c.pos)
	}
	buf := make([]byte, n)
	_, err := io.ReadFull(p.c, buf)
	return buf, err
}

// ebmlHeader 用 ebml-go 校验文件头 DocType。
type ebmlHeader struct {
	DocType string `ebml:"EBMLDocType"`
}

// Parse 流式解析整个输入，事件经 sink 上报；致命错误经返回值返回。
func (p *Parser) Parse(sink Sink) error {
	hdr, err := p.readHeader()
	if err != nil || hdr.ID != IDEBML {
		return &Error{Code: CodeBadHeader, Offset: 0, Msg: "missing EBML header"}
	}
	hb, err := p.readBytes(hdr.Size)
	if err != nil {
		return &Error{Code: CodeBadHeader, Offset: hdr.Offset, Msg: "truncated EBML header"}
	}
	var h ebmlHeader
	if err := ebmlgo.Unmarshal(bytesReader(hb), &h); err != nil ||
		(h.DocType != "matroska" && h.DocType != "webm") {
		return &Error{Code: CodeBadHeader, Offset: hdr.Offset, Msg: "unsupported EBML DocType"}
	}
	for {
		e, err := p.readHeader()
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
		if e.ID != IDSegment {
			if e.Size < 0 {
				return &Error{Code: CodeUnsupported, Offset: e.Offset, Msg: "unknown-size top element"}
			}
			if p.skip(e.Size) != nil {
				return nil
			}
			continue
		}
		if err := p.parseSegment(e, sink); err != nil {
			return err
		}
	}
}

func (p *Parser) parseSegment(seg *Element, sink Sink) error {
	end := int64(-1)
	if seg.Size >= 0 {
		end = seg.DataOffset + seg.Size
	}
	for end < 0 || p.c.pos < end {
		e, err := p.readHeader()
		if err != nil {
			if end < 0 && (err == io.EOF || err == io.ErrUnexpectedEOF) {
				return nil // 未知长度 Segment 至 EOF 结束
			}
			sink.OnError(&Error{Code: CodeTruncatedElement, Offset: p.c.pos, Msg: "segment body truncated"})
			return nil
		}
		if p.fileEnd >= 0 && e.Size >= 0 && e.DataOffset+e.Size > p.fileEnd {
			sink.OnError(&Error{Code: CodeTruncatedElement, Offset: e.Offset,
				Msg: fmt.Sprintf("element 0x%x declares %d bytes beyond end of file", e.ID, e.Size)})
			return nil // 截断元素之后无可恢复数据
		}
		switch e.ID {
		case IDInfo:
			p.parseInfo(e, sink)
		case IDTracks:
			p.parseTracks(e, sink)
		case IDCluster:
			p.parseCluster(e, sink)
		default: // Cues 等：跳过（丢失索引不影响块流恢复）
			if e.Size < 0 {
				sink.OnError(&Error{Code: CodeUnsupported, Offset: e.Offset, Msg: "unknown-size element, skip to EOF"})
				return nil
			}
			if p.skip(e.Size) != nil {
				return nil
			}
		}
	}
	return nil
}

func (p *Parser) parseInfo(e *Element, sink Sink) {
	data, err := p.readBytes(e.Size)
	if err != nil {
		sink.OnError(&Error{Code: CodeTruncatedElement, Offset: e.Offset, Msg: "info truncated"})
		return
	}
	scale := uint64(1000000) // Matroska 默认 1ms
	c := &countingReader{r: bytesReader(data)}
	for c.pos < int64(len(data)) {
		sub, err := readHeaderFrom(c)
		if err != nil {
			break
		}
		if sub.ID == IDTimestampScale {
			b, _ := readBytesFrom(c, sub.Size)
			scale = uintFromBytes(b)
		} else {
			skipN(c, sub.Size)
		}
	}
	sink.OnInfo(scale)
}
