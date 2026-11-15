package cli

import "math"

func nanFloat() float64 { return math.NaN() }
func infFloat() float64 { return math.Inf(1) }
