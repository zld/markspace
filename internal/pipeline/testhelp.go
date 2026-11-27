package pipeline

import "mediacore/internal/media"

func mediaSynthetic() media.Synthetic {
	return media.Synthetic{Width: 320, Height: 180, FPS: 30, Seconds: 1, Seed: 17}
}
