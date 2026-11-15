// relaycheck builds a two-end WebRTC link on loopback, generates RTP test
// packets on one end, forwards them through a local relay, and reports
// delivery, sequence, and jitter metrics on the other.
//
// Exit codes: 0 ok, 2 parameter error, 3 signaling/connection failure,
// 4 forwarding incomplete, 5 internal error.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"

	"relaycheck/internal/fixture"
	"relaycheck/internal/relay"
	"relaycheck/internal/signaling"
	"relaycheck/internal/transport"
)

const (
	exitOK = iota
	_
	exitParam
	exitConn
	exitIncomplete
	exitInternal
)

type runParams struct {
	packets         int
	payloadBytes    int
	interval        time.Duration
	bind            string
	port            int
	seqStart        uint
	timeout         time.Duration
	jsonOut         bool
	injectCandidate string
	clockRate       uint
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(exitParam)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(cmdRun(os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "error: unknown subcommand %q (want run or self-check)\n", os.Args[1])
		usage()
		os.Exit(exitParam)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: relaycheck <command>

commands:
  run         establish a loopback WebRTC link and relay RTP test packets
  self-check  run internal diagnostics for signaling, transport, and metrics

run flags: --packets --payload-bytes --interval --bind --port --seq-start
           --timeout --clock-rate --json --inject-candidate`)
}

func cmdRun(args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var p runParams
	fs.IntVar(&p.packets, "packets", 500, "number of RTP packets to generate")
	fs.IntVar(&p.payloadBytes, "payload-bytes", 480, "RTP payload size in bytes")
	fs.DurationVar(&p.interval, "interval", 5*time.Millisecond, "fixed send interval")
	fs.StringVar(&p.bind, "bind", "127.0.0.1", "loopback bind address for the forwarder")
	fs.IntVar(&p.port, "port", 0, "forwarder UDP port (0 = random)")
	fs.UintVar(&p.seqStart, "seq-start", 0, "first RTP sequence number")
	fs.DurationVar(&p.timeout, "timeout", 10*time.Second, "negotiation timeout")
	fs.UintVar(&p.clockRate, "clock-rate", fixture.DefaultClockRate, "RTP clock rate")
	fs.BoolVar(&p.jsonOut, "json", false, "emit the report as JSON")
	fs.StringVar(&p.injectCandidate, "inject-candidate", "", "test hook: inject a candidate addr:port into signaling")
	if err := fs.Parse(args); err != nil {
		return exitParam
	}
	if errs := validateParams(&p); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "error: %v\n", e)
		}
		return exitParam
	}
	return runPipeline(p)
}

func validateParams(p *runParams) []error {
	var errs []error
	cfg := fixture.Config{Count: p.packets, PayloadBytes: p.payloadBytes, ClockRate: uint32(p.clockRate)}
	if err := cfg.Validate(); err != nil {
		errs = append(errs, err)
	}
	if p.interval <= 0 {
		errs = append(errs, fmt.Errorf("interval: must be positive, got %s", p.interval))
	}
	if err := transport.ValidateLoopback(p.bind); err != nil {
		errs = append(errs, fmt.Errorf("bind: %v", err))
	}
	if p.port < 0 || p.port > 65535 {
		errs = append(errs, fmt.Errorf("port: out of range [0,65535], got %d", p.port))
	}
	if p.seqStart > 65535 {
		errs = append(errs, fmt.Errorf("seq-start: out of range [0,65535], got %d", p.seqStart))
	}
	if p.timeout <= 0 {
		errs = append(errs, fmt.Errorf("timeout: must be positive, got %s", p.timeout))
	}
	return errs
}

// fail prints a structured error and returns the exit code.
func fail(code int, jsonOut bool, phase string, err error) int {
	if jsonOut {
		b, _ := json.Marshal(map[string]any{"error": err.Error(), "phase": phase, "exit_code": code})
		fmt.Fprintln(os.Stderr, string(b))
	} else {
		fmt.Fprintf(os.Stderr, "error: phase=%s code=%d: %v\n", phase, code, err)
	}
	return code
}

// parseCandidateAddress extracts the address and port fields from an ICE
// candidate attribute string.
func parseCandidateAddress(cand string) (string, int, error) {
	f := strings.Fields(cand)
	if len(f) < 6 || !strings.HasPrefix(f[0], "candidate:") {
		return "", 0, fmt.Errorf("malformed candidate %q", cand)
	}
	port, err := strconv.Atoi(f[5])
	if err != nil {
		return "", 0, fmt.Errorf("malformed candidate port in %q", cand)
	}
	return f[4], port, nil
}

func newLoopbackAPI() *webrtc.API {
	se := webrtc.SettingEngine{}
	se.SetInterfaceFilter(func(name string) bool {
		iface, err := net.InterfaceByName(name)
		return err == nil && iface.Flags&net.FlagLoopback != 0
	})
	se.SetIPFilter(func(ip net.IP) bool { return ip.IsLoopback() })
	se.SetIncludeLoopbackCandidate(true)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4, webrtc.NetworkTypeUDP6})
	return webrtc.NewAPI(webrtc.WithSettingEngine(se))
}

func runPipeline(p runParams) (code int) {
	start := time.Now()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	fwd, err := relay.NewForwarder(p.bind, p.port, 4096, uint32(p.clockRate))
	if err != nil {
		return fail(exitConn, p.jsonOut, "bind", err)
	}
	// Cleanup runs on every path: close peer connections, then stop the
	// forwarder and wait for its goroutines.
	var offerPC, answerPC *webrtc.PeerConnection
	defer func() {
		if offerPC != nil {
			offerPC.Close()
		}
		if answerPC != nil {
			answerPC.Close()
		}
		fwd.Close()
	}()

	api := newLoopbackAPI()
	if offerPC, err = api.NewPeerConnection(webrtc.Configuration{}); err != nil {
		return fail(exitInternal, p.jsonOut, "setup", err)
	}
	if answerPC, err = api.NewPeerConnection(webrtc.Configuration{}); err != nil {
		return fail(exitInternal, p.jsonOut, "setup", err)
	}

	var sid [8]byte
	rand.Read(sid[:])
	sessionID := hex.EncodeToString(sid[:])
	offerPeer := signaling.NewPeer("offerer", 64)
	answerPeer := signaling.NewPeer("answerer", 64)
	defer offerPeer.Close()
	defer answerPeer.Close()
	var seq atomic.Uint64
	var rejected atomic.Int64
	ready := make(chan struct{}) // closed once both remote descriptions are set

	deliverCandidate := func(peer *signaling.Peer, pc *webrtc.PeerConnection, cand string) error {
		host, port, err := parseCandidateAddress(cand)
		if err != nil || !transport.IsLoopbackCandidate(net.JoinHostPort(host, strconv.Itoa(port))) {
			rejected.Add(1)
			if err == nil {
				err = fmt.Errorf("non-loopback candidate %s rejected", host)
			}
			return err
		}
		msg := signaling.Message{Version: signaling.ProtocolVersion, SessionID: sessionID,
			Seq: seq.Add(1), Type: signaling.TypeCandidate, Payload: cand}
		if err := peer.Deliver(msg); err != nil {
			return err
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
		return pc.AddICECandidate(webrtc.ICECandidateInit{Candidate: cand})
	}

	var candErr atomic.Value
	watch := func(peer *signaling.Peer, pc *webrtc.PeerConnection, src *webrtc.PeerConnection) {
		src.OnICECandidate(func(c *webrtc.ICECandidate) {
			if c == nil {
				return
			}
			if err := deliverCandidate(peer, pc, c.ToJSON().Candidate); err != nil {
				candErr.Store(err)
				cancel()
			}
		})
	}
	watch(answerPeer, answerPC, offerPC)
	watch(offerPeer, offerPC, answerPC)

	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus}, "audio", "relaycheck")
	if err != nil {
		return fail(exitInternal, p.jsonOut, "setup", err)
	}
	if _, err = offerPC.AddTrack(track); err != nil {
		return fail(exitInternal, p.jsonOut, "setup", err)
	}
	answerPC.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			pkt, _, err := tr.ReadRTP()
			if err != nil {
				return
			}
			fwd.Submit(pkt)
		}
	})

	if err := exchangeSDP(offerPC, answerPC, offerPeer, answerPeer, sessionID, &seq); err != nil {
		return fail(exitConn, p.jsonOut, "signaling", err)
	}
	close(ready)

	if p.injectCandidate != "" {
		host, portStr, _ := strings.Cut(p.injectCandidate, ":")
		port, _ := strconv.Atoi(portStr)
		cand := fmt.Sprintf("candidate:1 1 udp 2130706431 %s %d typ host", host, port)
		if err := deliverCandidate(answerPeer, answerPC, cand); err != nil {
			report := buildReport(fwd, 0, int(rejected.Load()), start, uint32(p.clockRate))
			emitReport(p, report)
			return fail(exitConn, p.jsonOut, "candidate-filter", err)
		}
	}

	connected := make(chan struct{})
	var once sync.Once
	offerPC.OnICEConnectionStateChange(func(s webrtc.ICEConnectionState) {
		if s == webrtc.ICEConnectionStateConnected || s == webrtc.ICEConnectionStateCompleted {
			once.Do(func() { close(connected) })
		}
	})
	select {
	case <-connected:
	case <-ctx.Done():
		if e, ok := candErr.Load().(error); ok {
			return fail(exitConn, p.jsonOut, "candidate-filter", e)
		}
		return fail(exitConn, p.jsonOut, "ice", fmt.Errorf("negotiation timed out or cancelled: %w", ctx.Err()))
	}

	pairs := countCandidatePairs(offerPC)
	sendErrs := sendStream(ctx, track, p)
	if sendErrs > 0 {
		fmt.Fprintf(os.Stderr, "error: phase=send: %d packet(s) failed to send\n", sendErrs)
	}
	waitDrain(ctx, fwd, p.packets)

	report := buildReport(fwd, pairs, int(rejected.Load()), start, uint32(p.clockRate))
	emitReport(p, report)
	if sendErrs > 0 || report.DroppedPackets > 0 || int(report.ForwardedPackets) != p.packets {
		return exitIncomplete
	}
	return exitOK
}

func exchangeSDP(offerPC, answerPC *webrtc.PeerConnection, offerPeer, answerPeer *signaling.Peer, sessionID string, seq *atomic.Uint64) error {
	offer, err := offerPC.CreateOffer(nil)
	if err != nil {
		return err
	}
	if err := offerPC.SetLocalDescription(offer); err != nil {
		return err
	}
	if err := offerPeer.MarkOffering(sessionID); err != nil {
		return err
	}
	msg := signaling.Message{Version: signaling.ProtocolVersion, SessionID: sessionID,
		Seq: seq.Add(1), Type: signaling.TypeOffer, Payload: offer.SDP}
	if err := answerPeer.Deliver(msg); err != nil {
		return err
	}
	if err := answerPC.SetRemoteDescription(offer); err != nil {
		return err
	}
	answer, err := answerPC.CreateAnswer(nil)
	if err != nil {
		return err
	}
	if err := answerPC.SetLocalDescription(answer); err != nil {
		return err
	}
	msg = signaling.Message{Version: signaling.ProtocolVersion, SessionID: sessionID,
		Seq: seq.Add(1), Type: signaling.TypeAnswer, Payload: answer.SDP}
	if err := offerPeer.Deliver(msg); err != nil {
		return err
	}
	return offerPC.SetRemoteDescription(answer)
}

func sendStream(ctx context.Context, track *webrtc.TrackLocalStaticRTP, p runParams) int {
	cfg := fixture.Config{Count: p.packets, PayloadBytes: p.payloadBytes,
		SeqStart: uint16(p.seqStart), SSRC: 0x5eb5, ClockRate: uint32(p.clockRate)}
	tsInc := uint32(p.interval.Seconds() * float64(p.clockRate))
	if tsInc == 0 {
		tsInc = 1
	}
	gen := fixture.NewGenerator(cfg, tsInc)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	sendErrs := 0
	for pkt := gen.Next(); pkt != nil; pkt = gen.Next() {
		select {
		case <-ctx.Done():
			return sendErrs + 1
		case <-ticker.C:
		}
		if err := track.WriteRTP(pkt); err != nil {
			sendErrs++
		}
	}
	return sendErrs
}

func waitDrain(ctx context.Context, fwd *relay.Forwarder, want int) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if int(fwd.Stats().Received) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
