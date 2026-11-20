#pragma once

namespace h264 {

// Runs the built-in test suite. Returns 0 when every check passes, 4 when a
// check fails. `verbose` prints each passing case name.
int run_self_tests(bool verbose);

}  // namespace h264
