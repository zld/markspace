package container

import (
	"bytes"

	mp4 "github.com/abema/go-mp4"

	"mq/internal/timeline"
)

// Clap 为自定义 clean aperture 盒（go-mp4 未内置），承载裁剪声明。
type Clap struct {
	mp4.Box
	CleanApertureWidthN  uint32 `mp4:"0,size=32"`
	CleanApertureWidthD  uint32 `mp4:"1,size=32"`
	CleanApertureHeightN uint32 `mp4:"2,size=32"`
	CleanApertureHeightD uint32 `mp4:"3,size=32"`
	HorizOffN            uint32 `mp4:"4,size=32"`
	HorizOffD            uint32 `mp4:"5,size=32"`
	VertOffN             uint32 `mp4:"6,size=32"`
	VertOffD             uint32 `mp4:"7,size=32"`
}

// GetType 返回盒类型。
func (*Clap) GetType() mp4.BoxType { return mp4.StrToBoxType("clap") }

func init() { mp4.AddBoxDef(&Clap{}) }

// IsMP4 判定 MP4 签名（首个盒须为已知顶层盒）。
func IsMP4(buf []byte) bool {
	if len(buf) < 8 {
		return false
	}
	switch string(buf[4:8]) {
	case "ftyp", "moov", "mdat", "free", "skip":
		return true
	}
	return false
}

// unmarshalBox 用 go-mp4 解码盒载荷。
func unmarshalBox(payload []byte, box mp4.IBox) error {
	_, err := mp4.Unmarshal(bytes.NewReader(payload), uint64(len(payload)), box, mp4.Context{})
	return err
}

// ParseMP4 解析 MP4 轨道元数据与样本表，保留每项来源偏移。
// 截断盒、非法长度或缺失必需字段时报告阶段与偏移并停止该素材。
func ParseMP4(buf []byte) (*Asset, error) {
	if !IsMP4(buf) {
		return nil, timeline.NewFatal(timeline.StageParse, 0,
			"unrecognized MP4 signature")
	}
	asset := &Asset{Format: "mp4", Raw: buf}
	c := NewCursor(buf)
	var moov *BoxHeader
	for !c.Done() {
		h, err := c.ReadBoxHeader("")
		if err != nil {
			return nil, err
		}
		if h.Type == "moov" {
			m := h
			moov = &m
		}
		if err := c.Skip(h.PayloadLen(), "", "box "+h.Type); err != nil {
			return nil, err
		}
	}
	if moov == nil {
		return nil, timeline.NewErr(timeline.StageParse, "", 0, "missing required moov box")
	}
	return asset, parseMoov(asset, buf[moov.Payload:moov.Payload+moov.PayloadLen()], moov.Payload)
}

// walkBoxes 遍历载荷内的子盒并对每个调用 fn。
func walkBoxes(payload []byte, base int64, track string, fn func(h BoxHeader, body []byte) error) error {
	c := NewCursor(payload)
	for !c.Done() {
		h, err := c.ReadBoxHeader(track)
		if err != nil {
			return err
		}
		h.Offset += base
		h.Payload += base
		body, _ := c.Read(h.PayloadLen(), track, "box "+h.Type)
		if err := fn(h, body); err != nil {
			return err
		}
	}
	return nil
}

func parseMoov(asset *Asset, payload []byte, base int64) error {
	return walkBoxes(payload, base, "", func(h BoxHeader, body []byte) error {
		if h.Type != "trak" {
			return nil
		}
		tm, err := parseTrak(body, h.Payload)
		if err != nil {
			return err
		}
		asset.Tracks = append(asset.Tracks, *tm)
		return nil
	})
}

func parseTrak(payload []byte, base int64) (*TrackMeta, error) {
	tm := &TrackMeta{Offsets: map[string]int64{}}
	err := walkBoxes(payload, base, "", func(h BoxHeader, body []byte) error {
		switch h.Type {
		case "tkhd":
			var tk mp4.Tkhd
			if err := unmarshalBox(body, &tk); err != nil {
				return timeline.NewErr(timeline.StageParse, "", h.Offset, "bad tkhd: %v", err)
			}
			tm.ID = int(tk.TrackID)
			tm.Width = int64(tk.Width >> 16)
			tm.Height = int64(tk.Height >> 16)
			tm.Rotation = rotationFromMatrix(tk.Matrix)
			tm.Offsets["rotation"] = h.Offset
		case "edts":
			return walkBoxes(body, h.Payload, "", func(eh BoxHeader, ebody []byte) error {
				if eh.Type != "elst" {
					return nil
				}
				var el mp4.Elst
				if err := unmarshalBox(ebody, &el); err != nil {
					return timeline.NewErr(timeline.StageParse, "", eh.Offset, "bad elst: %v", err)
				}
				if len(el.Entries) > 0 {
					// 子集语义：media_time 作为轨道非零起点。
					tm.Origin = int64(el.Entries[0].MediaTimeV0)
					tm.Offsets["origin"] = eh.Offset
				}
				return nil
			})
		case "mdia":
			return parseMdia(tm, body, h.Payload)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if tm.EditRate.Num <= 0 {
		return nil, timeline.NewErr(timeline.StageParse, tm.Name,
			tm.OffsetOf("edit_rate"), "missing required mdhd timescale")
	}
	if len(tm.Samples) == 0 && (tm.Kind == KindVideo || tm.Kind == KindAudio) {
		return nil, timeline.NewErr(timeline.StageParse, tm.Name,
			tm.OffsetOf("self"), "missing required sample table")
	}
	return tm, nil
}

func parseMdia(tm *TrackMeta, payload []byte, base int64) error {
	return walkBoxes(payload, base, "", func(h BoxHeader, body []byte) error {
		switch h.Type {
		case "mdhd":
			var md mp4.Mdhd
			if err := unmarshalBox(body, &md); err != nil {
				return timeline.NewErr(timeline.StageParse, "", h.Offset, "bad mdhd: %v", err)
			}
			tm.EditRate = timeline.NewRat(int64(md.Timescale), 1)
			tm.Duration = int64(md.DurationV0)
			tm.Offsets["edit_rate"] = h.Offset
		case "hdlr":
			var hd mp4.Hdlr
			if err := unmarshalBox(body, &hd); err != nil {
				return timeline.NewErr(timeline.StageParse, "", h.Offset, "bad hdlr: %v", err)
			}
			switch string(hd.HandlerType[:]) {
			case "vide":
				tm.Kind = KindVideo
			case "soun":
				tm.Kind = KindAudio
			default:
				tm.Kind = KindMeta
			}
		case "minf":
			return walkBoxes(body, h.Payload, "", func(mh BoxHeader, mbody []byte) error {
				if mh.Type == "stbl" {
					return parseStbl(tm, mbody, mh.Payload)
				}
				return nil
			})
		}
		return nil
	})
}

// rotationFromMatrix 将 tkhd 显示矩阵映射为 0/90/180/270 度。
func rotationFromMatrix(m [9]int32) int {
	const u = 0x10000
	switch [4]int32{m[0], m[1], m[3], m[4]} {
	case [4]int32{u, 0, 0, u}:
		return 0
	case [4]int32{0, u, -u, 0}:
		return 90
	case [4]int32{-u, 0, 0, -u}:
		return 180
	case [4]int32{0, -u, u, 0}:
		return 270
	}
	return 0
}
