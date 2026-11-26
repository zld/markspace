module replaykit

// github.com/pion/rtp v1.10.6 declares "go 1.24.0". The acceptance wrapper
// (go1.23.12) has GOTOOLCHAIN=auto, so it selects the cached toolchain that
// the pinned dependency requires while the commands stay unchanged.
go 1.24.0
