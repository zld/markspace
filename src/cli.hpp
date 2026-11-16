#ifndef TRANSITION_LAB_CLI_HPP
#define TRANSITION_LAB_CLI_HPP

#include <cstdint>
#include <string>
#include <vector>

#include "timeline.hpp"
#include "transition.hpp"

namespace tl {

enum class ExitCode : int {
    Ok = 0,
    Args = 2,
    Verify = 3,
    File = 4
};

struct DemoConfig {
    std::string out_dir;
    int width = 96;
    int height = 64;
    int fps = 24;
    int frames = 48;
    int transition_length = 0;  // 0 -> half of the total frames
    std::uint64_t seed = 17;
    bool verify = false;
    std::vector<std::string> speed_tokens;
    WipeDirection wipe_direction = WipeDirection::LeftToRight;
};

struct Options {
    bool show_help = false;
    bool valid = true;
    std::string error;
    std::string command;
    DemoConfig demo;
};

// Parse argv. On a parameter error `valid` is false and `error` carries
// a human-readable message; the caller exits with ExitCode::Args.
Options parse_args(int argc, char** argv);

// Turn the CLI speed tokens into validated nodes and build the speed
// map for `frames` output frames.
bool build_speed_map(const DemoConfig& config, SpeedMap* map,
                     std::string* error);

const char* wipe_direction_name(WipeDirection direction);

}  // namespace tl

#endif
