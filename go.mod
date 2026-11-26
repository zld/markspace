module rtpregress

go 1.23.0

require (
	github.com/abema/go-mp4 v1.7.3
	github.com/pion/rtp v1.10.6
)

require (
	github.com/google/uuid v1.1.2 // indirect
	github.com/pion/randutil v0.1.0 // indirect
)

replace github.com/pion/rtp v1.10.6 => ./third_party/pion-rtp
