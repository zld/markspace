package server

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/gortsplib/v4/pkg/liberrors"
	"github.com/pion/rtp"

	"rtspingest/internal/fsm"
	"rtspingest/internal/transport"
)

var errAnnounceRequired = errors.New("server: RECORD before a valid ANNOUNCE")

// Handler implements the subset of gortsplib.ServerHandler callbacks the
// ingest server needs. It drives the shared state machine and forwards media
// packets to the transport adapter selected for the run.
type Handler struct {
	mu sync.Mutex

	machine *fsm.Machine
	adapter transport.Adapter

	media  *description.Media
	format *format.H264
	ready  bool

	// firstErr holds the first protocol or state error observed on callbacks.
	firstErr error
	// closed is closed once OnSessionClose has run, signalling teardown.
	closed chan struct{}
}

// NewHandler wires a state machine and a transport adapter together.
func NewHandler(machine *fsm.Machine, adapter transport.Adapter) *Handler {
	return &Handler{
		machine: machine,
		adapter: adapter,
		closed:  make(chan struct{}),
	}
}

// Err returns the first error seen by the callbacks, if any.
func (h *Handler) Err() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.firstErr
}

// Done is closed when the RTSP session closes.
func (h *Handler) Done() <-chan struct{} { return h.closed }

func (h *Handler) recordErr(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.firstErr == nil {
		h.firstErr = err
	}
}

// OnConnOpen satisfies the gortsplib interface.
func (h *Handler) OnConnOpen(_ *gortsplib.ServerHandlerOnConnOpenCtx) {}

// OnConnClose records a transport-level connection error. A clean client
// disconnect surfaces as io.EOF or a closed-socket error and is expected
// after TEARDOWN, so it is ignored.
func (h *Handler) OnConnClose(ctx *gortsplib.ServerHandlerOnConnCloseCtx) {
	if ctx.Error == nil || errors.Is(ctx.Error, io.EOF) {
		return
	}
	if isBenignClose(ctx.Error) {
		return
	}
	h.recordErr(ctx.Error)
}

// isBenignClose reports operating-system level "connection closed" errors
// that accompany an orderly client shutdown.
func isBenignClose(err error) bool {
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		if errors.Is(netErr.Err, net.ErrClosed) {
			return true
		}
		return strings.Contains(netErr.Err.Error(), "closed")
	}
	return strings.Contains(err.Error(), "closed")
}

// OnSessionOpen satisfies the gortsplib interface.
func (h *Handler) OnSessionOpen(_ *gortsplib.ServerHandlerOnSessionOpenCtx) {}

// OnSessionClose drives the machine through Closing into Closed and releases
// the transport pipeline.
func (h *Handler) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	h.mu.Lock()
	wasReady := h.ready
	h.mu.Unlock()

	// a client TEARDOWN is the expected, successful end of the session, not
	// an error; only record unexpected failures.
	var tornDown liberrors.ErrServerSessionTornDown
	if ctx.Error != nil && !errors.As(ctx.Error, &tornDown) {
		h.recordErr(ctx.Error)
	}
	if wasReady {
		_ = h.machine.Handle(fsm.EventTeardown)
	}
	_ = h.machine.Handle(fsm.EventClosed)
	h.adapter.Close()
	close(h.closed)
}

// OnPacketLost is intentionally a no-op: sequence gaps are accounted for by
// the application reorder buffer, and implementing this callback also stops
// gortsplib from printing its own message to stderr.
func (h *Handler) OnPacketLost(_ *gortsplib.ServerHandlerOnPacketLostCtx) {}

// OnDecodeError swallows non-fatal decode noise; invalid media is counted by
// the H.264 reassembler instead.
func (h *Handler) OnDecodeError(_ *gortsplib.ServerHandlerOnDecodeErrorCtx) {}

// OnAnnounce validates the SDP and moves Created -> Announced.
func (h *Handler) OnAnnounce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	validated, err := ValidateDescription(ctx.Description)
	if err != nil {
		h.recordErr(err)
		return &base.Response{StatusCode: base.StatusUnsupportedMediaType}, err
	}

	if err := h.machine.Handle(fsm.EventAnnounce); err != nil {
		h.recordErr(err)
		return &base.Response{StatusCode: base.StatusBadRequest}, err
	}

	h.mu.Lock()
	h.media = validated.Media
	h.format = validated.Format
	h.mu.Unlock()

	return &base.Response{StatusCode: base.StatusOK}, nil
}

// OnSetup validates the negotiated transport and moves Announced -> Ready.
func (h *Handler) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if err := h.adapter.OnSetup(&ctx.Transport); err != nil {
		h.recordErr(err)
		return &base.Response{StatusCode: base.StatusBadRequest}, nil, err
	}
	if err := h.machine.Handle(fsm.EventSetup); err != nil {
		h.recordErr(err)
		return &base.Response{StatusCode: base.StatusBadRequest}, nil, err
	}

	h.mu.Lock()
	h.ready = true
	h.mu.Unlock()

	return &base.Response{StatusCode: base.StatusOK}, nil, nil
}

// OnRecord moves Ready -> Recording and registers the packet callback that
// feeds every received RTP packet into the transport's shared pipeline.
func (h *Handler) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	h.mu.Lock()
	media := h.media
	h.mu.Unlock()
	if media == nil {
		h.recordErr(errAnnounceRequired)
		return &base.Response{StatusCode: base.StatusBadRequest}, errAnnounceRequired
	}

	if err := h.machine.Handle(fsm.EventRecord); err != nil {
		h.recordErr(err)
		return &base.Response{StatusCode: base.StatusBadRequest}, err
	}

	ctx.Session.OnPacketRTPAny(func(_ *description.Media, _ format.Format, pkt *rtp.Packet) {
		h.adapter.OnPacket(pkt)
	})

	return &base.Response{StatusCode: base.StatusOK}, nil
}
