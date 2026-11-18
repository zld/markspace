#pragma once

#include <string>

#include "options.hpp"

namespace lutcheck {

// Parses argv into validated Options. Returns false and fills `error` on any
// malformed flag, missing value or out-of-range number; the caller reports it
// as ExitCode::Usage.
bool parse_options(int argc, char** argv, Options* options, std::string* error);

// Executes a validated command and returns the process exit code. Data and
// output failures are thrown as AppError.
int run(const Options& options);

std::string usage_text();

}  // namespace lutcheck
