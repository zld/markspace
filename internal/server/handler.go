package server

import (
	"errors"
	"sync"

	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
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

// OnConnClose records a transport-level connection error.
func (h *Handler) OnConnClose(ctx *gortsplib.ServerHandlerOnConnCloseCtx) {
	if ctx.Error != nil {
		h.recordErr(ctx.Error)
	}
}

// OnSessionOpen satisfies the gortsplib interface.
func (h *Handler) OnSessionOpen(_ *gortsplib.ServerHandlerOnSessionOpenCtx) {}

// OnSessionClose drives the machine through Closing into Closed and releases
// the transport pipeline.
func (h *Handler) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	h.mu.Lock()
	wasReady := h.ready
	h.mu.Unlock()

	if ctx.Error != nil {
		h.recordErr(ctx.Error)
	}
	if wasReady {
		_ = h.machine.Handle(fsm.EventTeardown)
	}
	_ = h.machine.Handle(fsm.EventClosed)
	h.adapter.Close()
	close(h.closed)
}

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
