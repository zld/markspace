// Package evidence 离屏导出帧序列摘要、生成时间清单并核验码率与对齐。
package evidence

import (
	"crypto/sha256"
	"encoding/binary"

	"mq/internal/timeline"
)

// FrameSummary 为离屏导出的单帧摘要：时间、尺寸、像素统计与哈希。
type FrameSummary struct {
	Index  int
	Time   timeline.Time // 主时基
	Width  int64
	Height int64
	Mean   float64 // 平均亮度 0..255
	Min    uint8
	Max    uint8
	Hash   [32]byte
}

// synthPixels 由样本字节确定性地合成离屏像素缓冲。
// 同一输入永远产生同一缓冲，保证重复执行哈希一致。
func synthPixels(sampleHash [32]byte, frameIdx int, n int) []byte {
	buf := make([]byte, n)
	for i := range buf {
		mix := uint32(i+1)*2654435761 ^ uint32(frameIdx)*2246822519
		buf[i] = sampleHash[i%32] ^ byte(mix>>24) ^ byte(mix>>8)
	}
	return buf
}

// ExportFrames 对合成时间线的全部视频样本离屏导出帧摘要。
// 不调用外部转码程序；像素由编码样本确定性合成。
func ExportFrames(tl *timeline.Timeline, asset *timeline.Asset) ([]FrameSummary, error) {
	var frames []FrameSummary
	idx := 0
	for ti := range tl.Tracks {
		tr := &tl.Tracks[ti]
		if tr.Meta.Kind != timeline.KindVideo {
			continue
		}
		w, h := tr.Transform.Width, tr.Transform.Height
		if w <= 0 || h <= 0 {
			return nil, timeline.NewErr(timeline.StageEvidence, tr.Meta.Name, -1,
				"invalid frame geometry %dx%d", w, h)
		}
		for _, s := range tr.Samples {
			data := asset.Raw[s.Offset : s.Offset+s.Size]
			sh := sha256.Sum256(data)
			buf := synthPixels(sh, idx, int(w*h))
			var sum uint64
			mn, mx := uint8(255), uint8(0)
			for _, p := range buf {
				sum += uint64(p)
				if p < mn {
					mn = p
				}
				if p > mx {
					mx = p
				}
			}
			frames = append(frames, FrameSummary{
				Index:  idx,
				Time:   s.Time,
				Width:  w,
				Height: h,
				Mean:   float64(sum) / float64(len(buf)),
				Min:    mn,
				Max:    mx,
				Hash:   hashFrame(s.Time, w, h, buf),
			})
			idx++
		}
	}
	return frames, nil
}

// hashFrame 计算帧哈希：覆盖时间、尺寸与像素内容。
func hashFrame(t timeline.Time, w, h int64, buf []byte) [32]byte {
	hw := sha256.New()
	var b [8]byte
	for _, v := range []int64{t.Units, t.Scale, w, h} {
		binary.BigEndian.PutUint64(b[:], uint64(v))
		hw.Write(b[:])
	}
	hw.Write(buf)
	var out [32]byte
	copy(out[:], hw.Sum(nil))
	return out
}

// FramesHash 计算帧序列的聚合哈希（顺序敏感，完全确定）。
func FramesHash(frames []FrameSummary) [32]byte {
	h := sha256.New()
	for _, f := range frames {
		h.Write(f.Hash[:])
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
