package mediacore

import (
	"fmt"
	"io"
)

// DemoOptions carries the command-line parameters.
type DemoOptions struct {
	Seed      int64
	LossBurst int
	Reorder   int
	Duplicate int
	Bandwidth []int
	Repeat    int
	WorkDir   string
	Output    io.Writer
}

// DemoRunner executes the acceptance scenario.
type DemoRunner struct {
	opts       DemoOptions
	out        io.Writer
	rtpStats   *RecoveryStats
	rtpPackets int
}

// NewDemoRunner returns a runner writing progress to out.
func NewDemoRunner(opts DemoOptions, out io.Writer) *DemoRunner {
	return &DemoRunner{opts: opts, out: out}
}

func (d *DemoRunner) p(format string, args ...interface{}) {
	fmt.Fprintf(d.out, format+"\n", args...)
}
