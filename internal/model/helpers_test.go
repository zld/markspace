package model

import "mediaqc/internal/clock"

// tb is a small helper shared by model tests.
func tb() clock.Timebase {
	tb, err := clock.New(90000)
	if err != nil {
		panic(err)
	}
	return tb
}
