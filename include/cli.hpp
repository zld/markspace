#pragma once

namespace mp4flow {
// Process entry point; argv excludes the program name. Returns the process
// exit code (0 success, 2 init incompat, 3 invalid box/sample layout,
// 4 missing expected sequence, 5 io/atomic publish failure).
int runCli(int argc, char** argv);
} // namespace mp4flow
