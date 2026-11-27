package fixture

import "errors"

var (
	errShortHeader = errors.New("fixture: payload shorter than media header")
	errBadMagic    = errors.New("fixture: media payload magic mismatch")
	errBadConfig   = errors.New("fixture: invalid stream configuration")
)
