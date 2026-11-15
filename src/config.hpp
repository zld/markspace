#pragma once

#include <string>

namespace sm {

enum class WrapPolicy { Space, Punctuation, Hard };

enum class ErrorKind { Config, Font, Output };

struct Error {
    ErrorKind kind = ErrorKind::Config;
    std::string message;
};

// Hard resource limits; inputs beyond these are rejected rather than
// allowed to exhaust memory or produce unbounded output files.
constexpr int kMaxSubtitles = 10000;
constexpr int kMaxSubtitleBytes = 65536;
constexpr long kMaxOutputBytes = 64L * 1024 * 1024;
constexpr int kMaxCanvasPx = 65535;
constexpr int kMaxFontSizePx = 1024;
constexpr double kMinScale = 0.01;
constexpr double kMaxScale = 100.0;

struct Config {
    std::string font_path;
    std::string input_path;
    std::string json_out;
    std::string make_fixture;  // when set, run in fixture generation mode

    int width = 0;             // canvas width in px (required in analysis)
    int height = 0;            // 0 => derived from line metrics
    int safe_left = 0;
    int safe_right = 0;
    int safe_top = 0;
    int safe_bottom = 0;
    int font_size_px = 0;
    double scale = 1.0;
    WrapPolicy wrap = WrapPolicy::Punctuation;
};

const char* wrapPolicyName(WrapPolicy policy);
bool parseWrapPolicy(const std::string& text, WrapPolicy& out);

// Parse raw CLI arguments. Unknown flags, missing values or malformed
// numbers produce an ErrorKind::Config error.
bool parseArgs(int argc, char** argv, Config& cfg, Error& error);

// Validate cross-field constraints (canvas vs. safe area, ranges,
// required paths). Font file *loading* is verified later by font_metrics.
bool validateConfig(const Config& cfg, Error& error);

}  // namespace sm
