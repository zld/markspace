module cmafrepair

go 1.23.0

toolchain go1.23.12

replace github.com/pion/rtp => ./third_party/pion-rtp

require (
	github.com/abema/go-mp4 v1.7.4
	github.com/pion/rtp v1.10.6
)

require (
	github.com/google/uuid v1.1.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
)
