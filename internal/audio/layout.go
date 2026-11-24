package audio

import "fmt"

// Layout 为声道布局。
type Layout int

const (
	LayoutMono Layout = iota + 1
	LayoutStereo
)

// ParseLayout 解析布局名。
func ParseLayout(s string) (Layout, error) {
	switch s {
	case "mono":
		return LayoutMono, nil
	case "stereo":
		return LayoutStereo, nil
	}
	return 0, fmt.Errorf("unknown layout %q", s)
}

func (l Layout) String() string {
	if l == LayoutStereo {
		return "stereo"
	}
	return "mono"
}

// Channels 返回声道数。
func (l Layout) Channels() int { return int(l) }

// MapChannels 把交错 PCM 从 from 声道数归一到目标布局（确定性）。
func MapChannels(in []int16, from int, to Layout) []int16 {
	tc := to.Channels()
	if from == tc {
		return in
	}
	n := len(in) / from
	out := make([]int16, 0, n*tc)
	switch {
	case from == 1 && tc == 2: // mono -> stereo：复制
		for _, s := range in {
			out = append(out, s, s)
		}
	case from == 2 && tc == 1: // stereo -> mono：均值
		for i := 0; i < n; i++ {
			out = append(out, int16((int32(in[2*i])+int32(in[2*i+1]))/2))
		}
	default: // 通用：缺失声道补 0，多余声道丢弃
		for i := 0; i < n; i++ {
			for c := 0; c < tc; c++ {
				if c < from {
					out = append(out, in[i*from+c])
				} else {
					out = append(out, 0)
				}
			}
		}
	}
	return out
}
