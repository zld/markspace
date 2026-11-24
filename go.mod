module mediapack

go 1.23

// v2.0.1 is the version requested by the task, but that tag is not published
// on the public module proxy. Pin the available v2 API release so the
// requested import path and APIs remain stable in an offline macOS workspace.
replace github.com/pion/rtp/v2 v2.0.1 => github.com/pion/rtp/v2 v2.0.0
