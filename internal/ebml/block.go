package ebml

import (
	"encoding/binary"
	"fmt"
	"io"
)

// 簇与块级解析：未知长度 Cluster 终止、损坏块隔离。

// clusterChild 判断 ID 是否可出现在 Cluster 内（用于未知长度 Cluster 终止）。
func clusterChild(id uint32) bool {
	switch id {
	case IDTimestamp, IDSimpleBlock, IDBlockGroup, IDBlock:
		return true
	}
	return false
}

func (p *Parser) parseCluster(cl *Element, sink Sink) {
	end := int64(-1)
	if cl.Size >= 0 {
		end = cl.DataOffset + cl.Size
	}
	var clusterTS int64
	hasTS := false
	for {
		if end >= 0 && p.c.pos >= end {
			return
		}
		e, err := p.peekHeader()
		if err != nil {
			if end < 0 && (err == io.EOF || err == io.ErrUnexpectedEOF) {
				return
			}
			sink.OnError(&Error{Code: CodeTruncatedElement, Offset: p.c.pos, Msg: "cluster body truncated"})
			return
		}
		if end < 0 && !clusterChild(e.ID) {
			return // 未知长度 Cluster 遇到非子元素即结束
		}
		p.readHeader()
		switch e.ID {
		case IDTimestamp:
			b, err := p.readBytes(e.Size)
			if err != nil {
				sink.OnError(&Error{Code: CodeTruncatedElement, Offset: e.Offset, Msg: "cluster timestamp truncated"})
				return
			}
			clusterTS = int64(uintFromBytes(b))
			hasTS = true
		case IDSimpleBlock:
			if !p.parseBlock(e, sink, clusterTS, hasTS, end) {
				return // 块损坏已隔离，簇剩余不可信
			}
		case IDBlockGroup:
			if !p.parseBlockGroup(e, sink, clusterTS, hasTS, end) {
				return
			}
		default:
			if e.Size < 0 || p.skip(e.Size) != nil {
				return
			}
		}
	}
}

// parseBlock 解析 SimpleBlock/Block；返回 false 表示块损坏且簇剩余不可信。
func (p *Parser) parseBlock(e *Element, sink Sink, clusterTS int64, hasTS bool, clusterEnd int64) bool {
	if clusterEnd >= 0 && e.DataOffset+e.Size > clusterEnd {
		sink.OnError(&Error{Code: CodeCorruptBlock, Offset: e.Offset,
			Msg: fmt.Sprintf("block size %d overruns cluster end", e.Size)})
		return false
	}
	data, err := p.readBytes(e.Size)
	if err != nil {
		sink.OnError(&Error{Code: CodeCorruptBlock, Offset: e.Offset, Msg: "block payload truncated"})
		return false
	}
	if len(data) < 4 {
		sink.OnError(&Error{Code: CodeCorruptBlock, Offset: e.Offset, Msg: "block too short"})
		return true
	}
	c := &countingReader{r: bytesReader(data)}
	track, _, err := readVINT(c, false)
	if err != nil || track == 0 {
		sink.OnError(&Error{Code: CodeCorruptBlock, Offset: e.Offset, Msg: "bad track number"})
		return true
	}
	rel := int16(binary.BigEndian.Uint16(data[c.pos : c.pos+2]))
	flags := data[c.pos+2]
	if flags&0x06 != 0 { // lacing 位
		sink.OnError(&Error{Code: CodeUnsupported, Offset: e.Offset, Msg: "laced block not supported"})
		return true
	}
	if !hasTS {
		sink.OnError(&Error{Code: CodeMissingTimestamp, Offset: e.Offset,
			Msg: "cluster missing Timestamp; deterministic correction applied"})
	}
	sink.OnBlock(Block{Track: track, ClusterTS: clusterTS, HasClusTS: hasTS,
		RelTS: rel, Data: data[c.pos+3:], Offset: e.Offset})
	return true
}

func (p *Parser) parseBlockGroup(e *Element, sink Sink, clusterTS int64, hasTS bool, clusterEnd int64) bool {
	end := e.DataOffset + e.Size
	for p.c.pos < end {
		sub, err := p.readHeader()
		if err != nil {
			sink.OnError(&Error{Code: CodeTruncatedElement, Offset: e.Offset, Msg: "block group truncated"})
			return false
		}
		if sub.ID == IDBlock {
			if !p.parseBlock(sub, sink, clusterTS, hasTS, clusterEnd) {
				return false
			}
		} else if sub.Size < 0 || p.skip(sub.Size) != nil {
			return false
		}
	}
	return true
}

