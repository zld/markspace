#pragma once

#include <string>
#include <vector>

#include "clock.hpp"
#include "media_types.hpp"
#include "synchronizer.hpp"

namespace pwsync {

struct CliOptions {
    bool generate_fixture = false;
    std::string fixture_path;
    std::string input_path;
    std::string output_path = "-";  // "-" => stdout
    bool json = false;
    ClockMode clock_mode = ClockMode::AudioMaster;
    double late_threshold_ms = 35.0;
    double path_a_delay_ms = 4.0;
    double path_b_delay_ms = 17.0;
    double discontinuity_tolerance_ms = 250.0;
    LatePolicy policy = LatePolicy::Drop;
    PipelineLimits limits{};
};

// Parses argv. Throws std::invalid_argument with a user-facing message on
// bad arguments; main() maps that to ExitCode::Usage.
CliOptions parseCli(int argc, char** argv);

void printUsage(std::ostream& out, const char* program);

// Runs the full decode -> simulate -> report pipeline. Returns the process
// exit code defined by the command line contract.
ExitCode runPipeline(const CliOptions& options, std::vector<MediaError>& errors);

}  // namespace pwsync
