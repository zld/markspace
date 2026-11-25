package engine

// routingState mirrors the PID lists currently published by the PSI tables.
// PIDs are kept sorted so a representation's index is stable across PMT
// version switches even when the elementary PID values themselves change.
type routingState struct {
	streamTypeVideo uint8
	videoPIDs       []uint16
	pcrPIDs         []uint16
}

// refresh pulls the current video and PCR PID sets out of the PSI tracker.
func (r *routingState) refresh(psi pidLister) {
	r.videoPIDs = psi.VideoPIDs(r.streamTypeVideo)
	r.pcrPIDs = psi.PCRPIDs()
}

// videoIndex maps a video PID to its representation index, or -1 when the
// PID is not one of the currently mapped video PIDs.
func (r *routingState) videoIndex(pid uint16) int {
	for i, p := range r.videoPIDs {
		if p == pid {
			return i
		}
	}
	return -1
}

// pcrIndex maps a PCR PID to its representation index, or -1 when unknown.
func (r *routingState) pcrIndex(pid uint16) int {
	for i, p := range r.pcrPIDs {
		if p == pid {
			return i
		}
	}
	return -1
}

// pidLister is the PSI surface the router depends on.
type pidLister interface {
	VideoPIDs(streamType uint8) []uint16
	PCRPIDs() []uint16
}
