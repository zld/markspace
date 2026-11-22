package timeline

import "fmt"

// Stage 标识处理流水线中的阶段，用于跨子系统定位错误。
type Stage string

const (
	StageParse    Stage = "parse"
	StageTimeline Stage = "timeline"
	StageStream   Stage = "stream"
	StageValidate Stage = "validate"
	StageEvidence Stage = "evidence"
)

// Error 为共享错误模型：阶段 + 轨道 + 原始字节偏移。
// Fatal 为 true 表示该素材完全不可处理（如签名未知、超出限制），
// 否则为可恢复失败，批次中其余素材不受影响。
type Error struct {
	Stage  Stage
	Track  string
	Offset int64 // 原始字节偏移，-1 表示不适用
	Fatal  bool
	Msg    string
}

func (e *Error) Error() string {
	loc := ""
	if e.Offset >= 0 {
		loc = fmt.Sprintf(" offset=%d", e.Offset)
	}
	return fmt.Sprintf("%s: track=%q%s: %s", e.Stage, e.Track, loc, e.Msg)
}

// NewErr 构造带阶段、轨道与偏移的错误。
func NewErr(stage Stage, track string, offset int64, format string, args ...any) *Error {
	return &Error{Stage: stage, Track: track, Offset: offset, Msg: fmt.Sprintf(format, args...)}
}

// NewFatal 构造致命错误（素材不可分类处理，如签名未知或超出限制）。
func NewFatal(stage Stage, offset int64, format string, args ...any) *Error {
	e := NewErr(stage, "", offset, format, args...)
	e.Fatal = true
	return e
}
