// Package audio 按统一时基完成采样率、声道布局与响度的确定性转换。
package audio

import "math"

// Stats 记录每轨转换前后的采样数、峰值与响度。
type Stats struct {
	Track      uint64  `json:"track"`
	InSamples  int     `json:"in_samples"`  // 每声道输入采样数
	OutSamples int     `json:"out_samples"` // 每声道输出采样数
	PeakIn     float64 `json:"peak_in"`
	PeakOut    float64 `json:"peak_out"`
	LUFSIn     float64 `json:"lufs_in"`
	LUFSOut    float64 `json:"lufs_out"`
}

// Resampler 为跨块连续的线性插值重采样器（确定性，无随机状态）。
type Resampler struct {
	InRate  int
	OutRate int
	chans   int
	next    float64 // 下一个输出采样对应的输入流位置（帧）
	inSeen  int     // 已消费的输入帧数
	emitted int     // 已产生的输出帧数
	prev    []int16 // 上一输入块的末帧
}

// NewResampler 构造重采样器。
func NewResampler(inRate, outRate, chans int) *Resampler {
	return &Resampler{InRate: inRate, OutRate: outRate, chans: chans}
}

func (r *Resampler) frameAt(in []int16, idx, c int) float64 {
	if idx < 0 {
		if r.prev != nil {
			return float64(r.prev[c])
		}
		return float64(in[c])
	}
	return float64(in[idx*r.chans+c])
}

// Process 重采样一块交错 PCM，返回交错输出；不足一帧的尾部分量留待下一块。
func (r *Resampler) Process(in []int16) []int16 {
	ch := r.chans
	nIn := len(in) / ch
	if nIn == 0 {
		return nil
	}
	step := float64(r.InRate) / float64(r.OutRate)
	var out []int16
	for {
		local := r.next - float64(r.inSeen)
		i := int(math.Floor(local))
		if i+1 > nIn-1 {
			break // 需要更多输入才能插值
		}
		frac := local - float64(i)
		for c := 0; c < ch; c++ {
			a := r.frameAt(in, i, c)
			b := r.frameAt(in, i+1, c)
			out = append(out, int16(math.Round(a+(b-a)*frac)))
		}
		r.next += step
		r.emitted++
	}
	r.inSeen += nIn
	r.prev = append(r.prev[:0], in[(nIn-1)*ch:]...)
	return out
}

// Flush 在输入结束时收尾：以末帧常量延拓，补齐到目标输出总数
// （round(inSeen × OutRate / InRate)），保证累计采样数确定。
func (r *Resampler) Flush() []int16 {
	ch := r.chans
	last := r.prev
	if last == nil {
		return nil
	}
	target := (r.inSeen*r.OutRate + r.InRate/2) / r.InRate
	var out []int16
	for r.emitted < target {
		for c := 0; c < ch; c++ {
			out = append(out, last[c])
		}
		r.emitted++
	}
	return out
}

// Peak 计算交错 PCM 的峰值（0..1）。
func Peak(pcm []int16) float64 {
	var m float64
	for _, s := range pcm {
		v := math.Abs(float64(s)) / 32768.0
		if v > m {
			m = v
		}
	}
	return m
}
