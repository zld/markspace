package main

import (
	"errors"

	"github.com/archive/mediakernel/internal/timeline"
)

func errInvalid() error  { return timeline.NewError(timeline.KindInvalidInput, "bad") }
func errLimit() error    { return timeline.NewError(timeline.KindLimit, "too big") }
func errCorrupt() error  { return timeline.NewError(timeline.KindCorrupt, "damaged") }
func errInternal() error { return timeline.NewError(timeline.KindInternal, "invariant") }
func errPlain() error    { return errors.New("unclassified failure") }
