// Package fixtures 自生成全部样例数据（MXF/MP4/故障样例），
// 不引用任何外部文件；同一参数永远产生同一字节序列。
package fixtures

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// pseudoBytes 以 sha256 计数器模式生成确定性伪随机字节。
func pseudoBytes(seed string, n int) []byte {
	out := make([]byte, 0, n)
	for counter := uint64(0); len(out) < n; counter++ {
		h := sha256.Sum256([]byte(fmt.Sprintf("%s/%d", seed, counter)))
		out = append(out, h[:]...)
	}
	return out[:n]
}

// bw 为确定性大端字节写入器。
type bw struct{ buf []byte }

func (w *bw) u8(v uint8)   { w.buf = append(w.buf, v) }
func (w *bw) u16(v uint16) { w.buf = binary.BigEndian.AppendUint16(w.buf, v) }
func (w *bw) u32(v uint32) { w.buf = binary.BigEndian.AppendUint32(w.buf, v) }
func (w *bw) i32(v int32)  { w.u32(uint32(v)) }
func (w *bw) bytes(b []byte) {
	w.buf = append(w.buf, b...)
}
func (w *bw) zeros(n int) { w.buf = append(w.buf, make([]byte, n)...) }

// fullbox 写入 version+flags 四字节。
func (w *bw) fullbox(version uint8, flags uint32) {
	w.u32(uint32(version)<<24 | flags&0xffffff)
}

// box 写入带长度前缀的盒。
func (w *bw) box(typ string, fill func(w *bw)) {
	start := len(w.buf)
	w.u32(0)
	w.bytes([]byte(typ))
	fill(w)
	binary.BigEndian.PutUint32(w.buf[start:], uint32(len(w.buf)-start))
}

// MP4Track 描述一条待生成的 MP4 轨道。
type MP4Track struct {
	ID          int
	Kind        string // "video" | "audio"
	Timescale   uint32
	Width       uint32
	Height      uint32
	Rotation    int
	Crop        *[4]uint32 // x, y, w, h
	Color       *[4]uint16 // primaries, transfer, matrix, fullrange
	Origin      int32      // elst media_time（非零起点）
	SampleDelta uint32
	SampleSizes []uint32
	Seed        string
}

// BuildMP4 生成确定性 MP4：ftyp + moov + mdat，一样本一 chunk。
func BuildMP4(tracks []MP4Track) []byte {
	payloads := make([][]byte, len(tracks))
	for i, t := range tracks {
		for j, sz := range t.SampleSizes {
			payloads[i] = append(payloads[i], pseudoBytes(fmt.Sprintf("%s/%d", t.Seed, j), int(sz))...)
		}
	}
	build := func(offsets [][]uint32) []byte {
		w := &bw{}
		w.box("ftyp", func(w *bw) {
			w.bytes([]byte("isom"))
			w.u32(0x200)
			w.bytes([]byte("isomiso2mp41"))
		})
		w.box("moov", func(w *bw) {
			w.box("mvhd", func(w *bw) { writeMvhd(w, tracks) })
			for i := range tracks {
				w.box("trak", func(w *bw) { writeTrak(w, tracks[i], offsets[i]) })
			}
		})
		w.box("mdat", func(w *bw) {
			for _, p := range payloads {
				w.bytes(p)
			}
		})
		return w.buf
	}
	zeros := make([][]uint32, len(tracks))
	for i := range tracks {
		zeros[i] = make([]uint32, len(tracks[i].SampleSizes))
	}
	first := build(zeros)
	// mdat 载荷起点 = 文件总长 - mdat 载荷长
	var payloadLen int
	for _, p := range payloads {
		payloadLen += len(p)
	}
	base := uint32(len(first) - payloadLen)
	offsets := make([][]uint32, len(tracks))
	cur := base
	for i, t := range tracks {
		offsets[i] = make([]uint32, len(t.SampleSizes))
		for j, sz := range t.SampleSizes {
			offsets[i][j] = cur
			cur += sz
		}
	}
	return build(offsets)
}

func trackDurationUnits(t MP4Track) uint32 {
	return t.SampleDelta * uint32(len(t.SampleSizes))
}

func movieDurationMs(tracks []MP4Track) uint32 {
	var max uint32
	for _, t := range tracks {
		d := trackDurationUnits(t) * 1000 / t.Timescale
		if d > max {
			max = d
		}
	}
	return max
}

func writeMvhd(w *bw, tracks []MP4Track) {
	w.fullbox(0, 0)
	w.u32(0)
	w.u32(0)
	w.u32(1000)
	w.u32(movieDurationMs(tracks))
	w.u32(0x00010000)
	w.u16(0x0100)
	w.u16(0)
	w.zeros(8)
	for _, v := range matrixFor(0) {
		w.i32(v)
	}
	w.zeros(24)
	w.u32(uint32(len(tracks)) + 1)
}

// matrixFor 返回指定角度的 tkhd 显示矩阵。
func matrixFor(rotation int) [9]int32 {
	const u = 0x10000
	switch ((rotation%360)+360)%360 {
	case 90:
		return [9]int32{0, u, 0, -u, 0, 0, 0, 0, 0x40000000}
	case 180:
		return [9]int32{-u, 0, 0, 0, -u, 0, 0, 0, 0x40000000}
	case 270:
		return [9]int32{0, -u, 0, u, 0, 0, 0, 0, 0x40000000}
	}
	return [9]int32{u, 0, 0, 0, u, 0, 0, 0, 0x40000000}
}

func writeTrak(w *bw, t MP4Track, stco []uint32) {
	durMs := trackDurationUnits(t) * 1000 / t.Timescale
	w.box("tkhd", func(w *bw) {
		w.fullbox(0, 3)
		w.u32(0)
		w.u32(0)
		w.u32(uint32(t.ID))
		w.u32(0)
		w.u32(durMs)
		w.zeros(8)
		w.u16(0)
		w.u16(0)
		if t.Kind == "audio" {
			w.u16(0x0100)
		} else {
			w.u16(0)
		}
		w.u16(0)
		for _, v := range matrixFor(t.Rotation) {
			w.i32(v)
		}
		w.u32(t.Width << 16)
		w.u32(t.Height << 16)
	})
	if t.Origin != 0 {
		w.box("edts", func(w *bw) {
			w.box("elst", func(w *bw) {
				w.fullbox(0, 0)
				w.u32(1)
				w.u32(durMs)
				w.i32(t.Origin)
				w.u16(1)
				w.u16(0)
			})
		})
	}
	w.box("mdia", func(w *bw) { writeMdia(w, t, stco) })
}

func writeMdia(w *bw, t MP4Track, stco []uint32) {
	w.box("mdhd", func(w *bw) {
		w.fullbox(0, 0)
		w.u32(0)
		w.u32(0)
		w.u32(t.Timescale)
		w.u32(trackDurationUnits(t))
		w.u16(0x55c4)
		w.u16(0)
	})
	handler := "vide"
	if t.Kind == "audio" {
		handler = "soun"
	}
	w.box("hdlr", func(w *bw) {
		w.fullbox(0, 0)
		w.u32(0)
		w.bytes([]byte(handler))
		w.zeros(12)
		w.bytes([]byte(handler + "-handler"))
		w.u8(0)
	})
	w.box("minf", func(w *bw) {
		if t.Kind == "audio" {
			w.box("smhd", func(w *bw) {
				w.fullbox(0, 0)
				w.u16(0)
				w.u16(0)
			})
		} else {
			w.box("vmhd", func(w *bw) {
				w.fullbox(0, 1)
				w.u16(0)
				w.zeros(6)
			})
		}
		w.box("dinf", func(w *bw) {
			w.box("dref", func(w *bw) {
				w.fullbox(0, 0)
				w.u32(1)
				w.box("url ", func(w *bw) { w.fullbox(0, 1) })
			})
		})
		w.box("stbl", func(w *bw) { writeStbl(w, t, stco) })
	})
}

func writeStbl(w *bw, t MP4Track, stco []uint32) {
	w.box("stsd", func(w *bw) {
		w.fullbox(0, 0)
		w.u32(1)
		if t.Kind == "audio" {
			w.box("mp4a", func(w *bw) {
				w.zeros(6)
				w.u16(1)
				w.zeros(8)
				w.u16(2)
				w.u16(16)
				w.u16(0)
				w.u16(0)
				w.u32(t.Timescale << 16)
			})
		} else {
			w.box("avc1", func(w *bw) {
				w.zeros(6)
				w.u16(1)
				w.zeros(16)
				w.u16(uint16(t.Width))
				w.u16(uint16(t.Height))
				w.u32(0x00480000)
				w.u32(0x00480000)
				w.u32(0)
				w.u16(1)
				w.zeros(32)
				w.u16(0x18)
				w.u16(0xffff)
				if t.Color != nil {
					w.box("colr", func(w *bw) {
						w.bytes([]byte("nclx"))
						w.u16(t.Color[0])
						w.u16(t.Color[1])
						w.u16(t.Color[2])
						w.u8(uint8(t.Color[3]) << 7)
					})
				}
				if t.Crop != nil {
					w.box("clap", func(w *bw) {
						w.u32(t.Crop[2]) // width N
						w.u32(1)
						w.u32(t.Crop[3]) // height N
						w.u32(1)
						w.u32(t.Crop[0]) // horiz off N
						w.u32(1)
						w.u32(t.Crop[1]) // vert off N
						w.u32(1)
					})
				}
			})
		}
	})
	w.box("stts", func(w *bw) {
		w.fullbox(0, 0)
		w.u32(1)
		w.u32(uint32(len(t.SampleSizes)))
		w.u32(t.SampleDelta)
	})
	w.box("stsc", func(w *bw) {
		w.fullbox(0, 0)
		w.u32(1)
		w.u32(1)
		w.u32(1)
		w.u32(1)
	})
	w.box("stsz", func(w *bw) {
		w.fullbox(0, 0)
		w.u32(0)
		w.u32(uint32(len(t.SampleSizes)))
		for _, s := range t.SampleSizes {
			w.u32(s)
		}
	})
	w.box("stco", func(w *bw) {
		w.fullbox(0, 0)
		w.u32(uint32(len(stco)))
		for _, o := range stco {
			w.u32(o)
		}
	})
}
