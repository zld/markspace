package mediaerr

// Hard safety limits. Inputs beyond these are rejected with CatValidation so
// the kernel never claims to handle unbounded material.
const (
	MaxDurationTick    = uint64(90000 * 60 * 30) // 30 minutes on the 90 kHz clock
	MaxSegments        = 1024                    // segments per representation
	MaxRepresentations = 16
	MaxRTPpacketSize   = 1500 // bytes; Ethernet MTU-class RTP payload
	MaxWorkers         = 64
	MaxQueue           = 4096
	MaxReorderWindow   = 4096 // sequence slots before a gap becomes unrecoverable
	MaxStreams         = 32
)

// CheckWorkerQueue validates pool configuration against the hard caps.
func CheckWorkerQueue(workers, queue int) error {
	if workers <= 0 {
		return New(CatValidation, "workers_zero", "worker count must be positive")
	}
	if workers > MaxWorkers {
		return New(CatValidation, "workers_cap", "worker count exceeds hard limit").Wrap(ErrLimit)
	}
	if queue <= 0 {
		return New(CatValidation, "queue_zero", "queue capacity must be positive")
	}
	if queue > MaxQueue {
		return New(CatValidation, "queue_cap", "queue capacity exceeds hard limit").Wrap(ErrLimit)
	}
	return nil
}
