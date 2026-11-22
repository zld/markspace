package container

import (
	"bytes"
	"encoding/binary"
	"strconv"

	"mq/internal/timeline"
)

// MXF 元数据子集的 UL 键注册表（自生成样例专用，遵循 KLV 结构）。
var (
	ulHeaderPartition = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0d, 0x01, 0x02, 0x01, 0x01, 0x02, 0x04, 0x00}
	ulTrackMeta       = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01, 0x48, 0x00}
	ulColorDescriptor = []byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x53, 0x01, 0x01, 0x0d, 0x01, 0x01, 0x01, 0x01, 0x01, 0x29, 0x00}
	ulEssence         = []byte{0x06, 0x0e, 0x2b, 0x34, 0x01, 0x02, 0x01, 0x01, 0x0d, 0x01, 0x03, 0x01, 0x15, 0x01, 0x05, 0x00}
)

// IsMXF 判定 MXF 签名（首 KLV 须为头部分区包）。
func IsMXF(buf []byte) bool {
	return len(buf) >= 16 && bytes.Equal(buf[:16], ulHeaderPartition)
}

// 轨道元数据 KLV 内的本地集字段标签。
const (
	mxfTagTrackID   = 0x01
	mxfTagKind      = 0x02
	mxfTagRateNum   = 0x03
	mxfTagRateDen   = 0x04
	mxfTagOrigin    = 0x05
	mxfTagDuration  = 0x06
	mxfTagWidth     = 0x07
	mxfTagHeight    = 0x08
	mxfTagName      = 0x09
	mxfTagRotation  = 0x0a
	mxfTagCropX     = 0x0b
	mxfTagCropY     = 0x0c
	mxfTagCropW     = 0x0d
	mxfTagCropH     = 0x0e
	mxfTagPrimaries = 0x0f
	mxfTagTransfer  = 0x10
	mxfTagMatrix    = 0x11
	mxfTagFullRange = 0x12
	mxfTagSampleDur = 0x13
)

// readBERLength 读取 BER 长度（短形式或 1..8 字节长形式）。
func readBERLength(c *Cursor) (int64, error) {
	off := c.Offset()
	b, err := c.U8("", "BER length")
	if err != nil {
		return 0, err
	}
	if b < 0x80 {
		return int64(b), nil
	}
	n := int(b & 0x7f)
	if n < 1 || n > 8 {
		return 0, timeline.NewErr(timeline.StageParse, "", off,
			"illegal BER long form %d bytes", n)
	}
	raw, err := c.Read(int64(n), "", "BER long form")
	if err != nil {
		return 0, err
	}
	var v uint64
	for _, x := range raw {
		v = v<<8 | uint64(x)
	}
	if v > uint64(1<<62) {
		return 0, timeline.NewErr(timeline.StageParse, "", off,
			"illegal BER length %d", v)
	}
	return int64(v), nil
}

// ParseMXF 解析 MXF 元数据子集：KLV 序列 -> 统一轨道视图。
// 每个字段记录来源偏移；KLV 声明长度越界时报告该 KLV 的偏移并停止该素材。
func ParseMXF(buf []byte) (*Asset, error) {
	if !IsMXF(buf) {
		return nil, timeline.NewFatal(timeline.StageParse, 0,
			"unrecognized MXF signature")
	}
	asset := &Asset{Format: "mxf", Raw: buf}
	byID := map[uint32]*TrackMeta{}
	durByID := map[uint32]int64{}
	c := NewCursor(buf)
	for !c.Done() {
		klvOff := c.Offset()
		key, err := c.Read(16, "", "KLV key")
		if err != nil {
			return nil, err
		}
		length, err := readBERLength(c)
		if err != nil {
			return nil, err
		}
		if length > c.Remaining() {
			return nil, timeline.NewErr(timeline.StageParse, "", klvOff,
				"mxf metadata overrun: KLV declares %d bytes, %d remain",
				length, c.Remaining())
		}
		payloadOff := c.Offset()
		payload, _ := c.Read(length, "", "KLV value")
		switch {
		case bytes.Equal(key, ulHeaderPartition):
			// 分区包：子集内无需解释。
		case bytes.Equal(key, ulTrackMeta):
			tm, sampleDur, err := parseMXFTrackMeta(payload, klvOff, payloadOff)
			if err != nil {
				return nil, err
			}
			asset.Tracks = append(asset.Tracks, *tm)
			byID[uint32(tm.ID)] = &asset.Tracks[len(asset.Tracks)-1]
			durByID[uint32(tm.ID)] = sampleDur
		case bytes.Equal(key, ulColorDescriptor):
			if err := parseMXFColor(payload, payloadOff, byID); err != nil {
				return nil, err
			}
		case bytes.Equal(key, ulEssence):
			if err := parseMXFEssence(payload, payloadOff, byID, durByID); err != nil {
				return nil, err
			}
		default:
			// 未知键：跳过；必需字段缺失在校验阶段报告。
		}
	}
	for i := range asset.Tracks {
		t := &asset.Tracks[i]
		if t.EditRate.Den == 0 || t.EditRate.Num <= 0 {
			return nil, timeline.NewErr(timeline.StageParse, t.Name,
				t.OffsetOf("self"), "missing required edit rate")
		}
	}
	return asset, nil
}

// parseMXFTrackMeta 解析轨道元数据本地集（tag/len/value 字段序列），
// 返回轨道视图与轨道级样本时长。
func parseMXFTrackMeta(payload []byte, klvOff, payloadOff int64) (*TrackMeta, int64, error) {
	tm := &TrackMeta{Offsets: map[string]int64{"self": klvOff}}
	var crop CropRect
	var haveCrop bool
	var sampleDur int64 = 1
	c := NewCursor(payload)
	for !c.Done() {
		fieldOff := payloadOff + c.Offset()
		tag, err := c.U16("", "field tag")
		if err != nil {
			return nil, 0, err
		}
		flen, err := c.U16("", "field length")
		if err != nil {
			return nil, 0, err
		}
		val, err := c.Read(int64(flen), "", "field value")
		if err != nil {
			return nil, 0, err
		}
		u32 := func() uint32 {
			if len(val) < 4 {
				return 0
			}
			return binary.BigEndian.Uint32(val)
		}
		u64 := func() int64 {
			if len(val) < 8 {
				return 0
			}
			return int64(binary.BigEndian.Uint64(val))
		}
		switch int(tag) {
		case mxfTagTrackID:
			tm.ID = int(u32())
		case mxfTagKind:
			if len(val) > 0 {
				tm.Kind = map[uint8]string{1: KindVideo, 2: KindAudio, 3: KindMeta}[val[0]]
			}
		case mxfTagRateNum:
			tm.EditRate.Num = int64(u32())
			tm.Offsets["edit_rate"] = fieldOff
		case mxfTagRateDen:
			tm.EditRate.Den = int64(u32())
		case mxfTagOrigin:
			tm.Origin = u64()
			tm.Offsets["origin"] = fieldOff
		case mxfTagDuration:
			tm.Duration = u64()
		case mxfTagWidth:
			tm.Width = int64(u32())
		case mxfTagHeight:
			tm.Height = int64(u32())
		case mxfTagName:
			tm.Name = string(val)
		case mxfTagRotation:
			if len(val) >= 2 {
				tm.Rotation = int(int16(binary.BigEndian.Uint16(val)))
			}
			tm.Offsets["rotation"] = fieldOff
		case mxfTagCropX:
			crop.X = int64(u32())
			haveCrop = true
			tm.Offsets["crop"] = fieldOff
		case mxfTagCropY:
			crop.Y = int64(u32())
		case mxfTagCropW:
			crop.W = int64(u32())
		case mxfTagCropH:
			crop.H = int64(u32())
		case mxfTagPrimaries:
			tm.Color.Primaries = uint16(u32() & 0xffff)
			tm.Color.Source = "mxf_track"
			tm.Offsets["color"] = fieldOff
		case mxfTagTransfer:
			tm.Color.Transfer = uint16(u32() & 0xffff)
		case mxfTagMatrix:
			tm.Color.Matrix = uint16(u32() & 0xffff)
		case mxfTagFullRange:
			if len(val) > 0 {
				tm.Color.FullRange = val[0] != 0
			}
		case mxfTagSampleDur:
			sampleDur = int64(u32())
		default:
			// 未知可选字段：跳过。
		}
	}
	if tm.Name == "" {
		tm.Name = "mxf-track-" + strconv.Itoa(tm.ID)
	}
	if haveCrop {
		c := crop
		tm.Crop = &c
	}
	tm.EditRate = timeline.NewRat(tm.EditRate.Num, tm.EditRate.Den)
	return tm, sampleDur, nil
}

// parseMXFColor 解析色彩描述符 KLV：trackID + primaries/transfer/matrix/fullrange。
func parseMXFColor(payload []byte, payloadOff int64, byID map[uint32]*TrackMeta) error {
	if len(payload) < 11 {
		return timeline.NewErr(timeline.StageParse, "", payloadOff,
			"color descriptor too short: %d bytes", len(payload))
	}
	id := binary.BigEndian.Uint32(payload)
	tm, ok := byID[id]
	if !ok {
		return timeline.NewErr(timeline.StageParse, "", payloadOff,
			"color descriptor references unknown track %d", id)
	}
	tm.AltColor = ColorInfo{
		Primaries: binary.BigEndian.Uint16(payload[4:]),
		Transfer:  binary.BigEndian.Uint16(payload[6:]),
		Matrix:    binary.BigEndian.Uint16(payload[8:]),
		FullRange: payload[10] != 0,
		Source:    "mxf_descriptor",
	}
	tm.Offsets["alt_color"] = payloadOff
	return nil
}

// parseMXFEssence 解析 essence KLV：trackID + index + 样本载荷。
func parseMXFEssence(payload []byte, payloadOff int64, byID map[uint32]*TrackMeta, durByID map[uint32]int64) error {
	if len(payload) < 8 {
		return timeline.NewErr(timeline.StageParse, "", payloadOff,
			"essence item too short: %d bytes", len(payload))
	}
	id := binary.BigEndian.Uint32(payload)
	tm, ok := byID[id]
	if !ok {
		return timeline.NewErr(timeline.StageParse, "", payloadOff,
			"essence references unknown track %d", id)
	}
	dur := durByID[id]
	if dur <= 0 {
		dur = 1
	}
	tm.Samples = append(tm.Samples, SampleMeta{
		Index:    len(tm.Samples),
		Duration: dur,
		Size:     int64(len(payload)) - 8,
		Offset:   payloadOff + 8,
	})
	return nil
}
