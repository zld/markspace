package pipeline

import (
	"os"

	"media-archiver/internal/media"
)

func hashFileStat(path string) (string, int64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	sha, err := hashFile(path)
	if err != nil {
		return "", st.Size(), err
	}
	return sha, st.Size(), nil
}

func hashCanonicalOrPanic(v any) string {
	data, err := media.MarshalCanonicalJSON(v)
	if err != nil {
		panic(err)
	}
	return hashBytes(data)
}

func interrupt(workdir string, man *Manifest, rep *Report, stage media.Stage) (*Report, error) {
	man.Status = "interrupted"
	_ = saveManifest(workdir, man)
	me := media.NewError(media.CodeInterrupted, stage, -1, "interrupted after completed stage", nil)
	rep.Status = "interrupted"
	rep.ErrorCode = me.Code
	rep.Stage = stage
	rep.CorruptOffset = -1
	return rep, me
}

func failManifest(workdir string, man *Manifest, rep *Report, err error) (*Report, error) {
	me := media.AsError(err)
	if me == nil {
		me = media.NewError(media.CodeInternal, media.StageVerify, -1, err.Error(), err)
	}
	if man != nil {
		man.Status = "rejected"
		man.Failure = &Failure{
			ErrorCode: string(me.Code),
			Stage:     string(me.Stage),
			Offset:    me.Offset,
			Reason:    me.Reason,
		}
		_ = saveManifest(workdir, man)
	}
	if rep != nil {
		rep.Status = "rejected"
		rep.ErrorCode = me.Code
		rep.Stage = me.Stage
		rep.CorruptOffset = me.Offset
	}
	return rep, me
}

func firstCorruption(blocks []media.CanonicalBlock) (int64, string) {
	for _, b := range blocks {
		if b.Bad && b.CorruptOffset >= 0 {
			reason := b.CorruptionReason
			if reason == "" {
				reason = "corrupt PCM block"
			}
			return b.CorruptOffset, reason
		}
	}
	return -1, "corrupt PCM block"
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func max64(values ...int64) int64 {
	m := values[0]
	for _, v := range values[1:] {
		if v > m {
			m = v
		}
	}
	return m
}
