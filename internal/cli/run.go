package cli

import (
	"fmt"
	"io"
	"net"

	"jbcheck/internal/ingress"
	"jbcheck/internal/jitter"
	"jbcheck/internal/metrics"
	"jbcheck/internal/packet"
	"jbcheck/internal/plc"
	"jbcheck/internal/sim"
)

// Main is the program entry point; it returns the process exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "demo" {
		fmt.Fprintln(stderr, "usage: jbcheck demo [--path trace|udp] [flags]")
		return ExitArgs
	}
	o, err := Parse(args[1:])
	if err != nil {
		fmt.Fprintln(stderr, "jbcheck: argument error:", err)
		return ExitArgs
	}
	return Run(o, stdout, stderr)
}

// Run executes one demo run and writes the report.
func Run(o Options, stdout, stderr io.Writer) int {
	events := sim.Generate(sim.Config{
		Seed:      o.Seed,
		Frames:    o.Frames,
		Loss:      o.Loss,
		Burst:     o.Burst,
		Reorder:   o.Reorder,
		Duplicate: o.Duplicate,
		JitterMs:  o.JitterMs,
	})
	st := metrics.New(o.Curve.Milliseconds())
	ctrl := jitter.NewController(jitter.DefaultConfig(), st, plc.New(o.Seed))

	switch o.Path {
	case "trace":
		ingress.RunTrace(events, ctrl, st)
		if st.Input == 0 && st.BadPackets > 0 {
			fmt.Fprintln(stderr, "jbcheck: trace corrupt: no decodable packet")
			return ExitCorrupt
		}
	case "udp":
		if code := runUDP(o, events, ctrl, st, stderr); code != ExitOK {
			return code
		}
	}

	var err error
	if o.Format == "csv" {
		err = st.WriteCSV(stdout)
	} else {
		err = st.WriteText(stdout)
	}
	if err != nil {
		fmt.Fprintln(stderr, "jbcheck: writing report:", err)
		return ExitIO
	}
	return ExitOK
}

func runUDP(o Options, events []sim.Event, ctrl *jitter.Controller, st *metrics.Stats, stderr io.Writer) int {
	addr, err := net.ResolveUDPAddr("udp", o.Listen)
	if err != nil || addr.IP == nil || !addr.IP.IsLoopback() {
		fmt.Fprintf(stderr, "jbcheck: --listen %q is not a loopback UDP address\n", o.Listen)
		return ExitArgs
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		fmt.Fprintln(stderr, "jbcheck: listen:", err)
		return ExitIO
	}
	defer conn.Close()
	if err := ingress.RunUDP(conn, events, ctrl, st); err != nil {
		fmt.Fprintln(stderr, "jbcheck: udp I/O:", err)
		return ExitIO
	}
	if st.Input == 0 && st.BadPackets > 0 {
		fmt.Fprintln(stderr, "jbcheck: input corrupt: no decodable packet")
		return ExitCorrupt
	}
	return ExitOK
}

// PacketMax is exported for documentation of the receive bound.
const PacketMax = packet.MaxPacket
