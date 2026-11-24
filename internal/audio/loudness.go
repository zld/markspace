package audio

import "math"

// MeasureLUFS 以 RMS 近似测量响度（LUFS = -0.691 + 20·log10(rms)）。
// 确定性、整块一次计算，适用于本地归档恢复场景。
func MeasureLUFS(pcm []int16) float64 {
	if len(pcm) == 0 {
		return -144
	}
	var sum float64
	for _, s := range pcm {
		v := float64(s) / 32768.0
		sum += v * v
	}
	rms := math.Sqrt(sum / float64(len(pcm)))
	if rms <= 0 {
		return -144
	}
	return -0.691 + 20*math.Log10(rms)
}

// GainFor 计算把 current LUFS 归一到 target 所需的线性增益。
func GainFor(current, target float64) float64 {
	return math.Pow(10, (target-current)/20)
}

// ApplyGain 对交错 PCM 施加固定增益并硬限幅到 int16（确定性）。
func ApplyGain(pcm []int16, gain float64) {
	for i, s := range pcm {
		v := math.Round(float64(s) * gain)
		if v > 32767 {
			v = 32767
		} else if v < -32768 {
			v = -32768
		}
		pcm[i] = int16(v)
	}
}
