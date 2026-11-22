package timeline

// 轨道类别。
const (
	KindVideo = "video"
	KindAudio = "audio"
	KindMeta  = "meta"
)

// SampleMeta 描述一个编码样本的位置与时长，重封装时原样保留。
type SampleMeta struct {
	Index    int
	Duration int64 // 以轨道时基计
	Size     int64
	Offset   int64 // 样本载荷在源文件中的原始字节偏移
}

// CropRect 为裁剪矩形（源文件声明的原始值，像素单位）。
type CropRect struct {
	X, Y, W, H int64
}

// ColorInfo 为一份色彩描述及其来源标识，来源用于冲突裁决。
type ColorInfo struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
	Source    string // 如 mp4_colr / mxf_track / mxf_descriptor
}

// Zero 表示未声明色彩信息。
func (c ColorInfo) Zero() bool {
	return c.Primaries == 0 && c.Transfer == 0 && c.Matrix == 0
}

// TrackMeta 为跨容器统一的轨道元数据，每项字段保留来源偏移。
type TrackMeta struct {
	ID       int
	Name     string
	Kind     string
	EditRate Rat // 轨道时基：刻度/秒
	Origin   int64
	Duration int64 // 以轨道时基计
	Width    int64
	Height   int64
	Rotation int // 声明的旋转角度（度）
	Crop     *CropRect
	Color    ColorInfo // 轨道级色彩声明
	AltColor ColorInfo // 描述符级色彩声明（可能与轨道级冲突）
	Samples  []SampleMeta
	Offsets  map[string]int64 // 字段名 -> 源文件字节偏移（可追溯）
}

// OffsetOf 返回字段的来源偏移，缺失时返回 -1。
func (t *TrackMeta) OffsetOf(field string) int64 {
	if t.Offsets == nil {
		return -1
	}
	if v, ok := t.Offsets[field]; ok {
		return v
	}
	return -1
}

// Asset 为一个解析后的素材：统一轨道视图 + 原始字节（样本零拷贝引用）。
type Asset struct {
	Format string // "mxf" | "mp4"
	Tracks []TrackMeta
	Raw    []byte
}

// SampleBytes 返回样本载荷的只读视图。
func (a *Asset) SampleBytes(s SampleMeta) []byte {
	return a.Raw[s.Offset : s.Offset+s.Size]
}
