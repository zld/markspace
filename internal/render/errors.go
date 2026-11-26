package render

import "replayctl/internal/fault"

func errAudioRange(path string, tick int64) error {
	return fault.New(fault.SampleOffset, fault.StageRender, path, -1, tick, "source audio tick outside PCM index")
}

func errNoAudio(tick int64) error {
	return fault.New(fault.TimeGap, fault.StageRender, "", -1, tick, "no audio clip at tick")
}

func errTransitionMissing(tick int64) error {
	return fault.New(fault.Render, fault.StageRender, "", -1, tick, "active transition clips missing")
}
