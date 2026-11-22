package app

import "sync/atomic"

func addActive(p *int32, delta int32) { atomic.AddInt32(p, delta) }
func loadActive(p *int32) int32       { return atomic.LoadInt32(p) }
