package clock

import "mediapack/internal/model"

func errZeroTimescale() error {
	return model.NewFieldError(model.KindInvalidInput, "", -1,
		"timescale", "track timescale must be non-zero")
}
