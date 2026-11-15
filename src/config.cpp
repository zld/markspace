#include "config.hpp"

#include <cerrno>
#include <cstdlib>
#include <limits>

namespace sm {

const char* wrapPolicyName(WrapPolicy policy) {
    switch (policy) {
        case WrapPolicy::Space:
            return "space";
        case WrapPolicy::Punctuation:
            return "punctuation";
        case WrapPolicy::Hard:
            return "hard";
    }
    return "punctuation";
}

bool parseWrapPolicy(const std::string& text, WrapPolicy& out) {
    if (text == "space") {
        out = WrapPolicy::Space;
    } else if (text == "punctuation") {
        out = WrapPolicy::Punctuation;
    } else if (text == "hard") {
        out = WrapPolicy::Hard;
    } else {
        return false;
    }
    return true;
}

namespace {

bool parsePositiveInt(const std::string& s, int& out, bool allow_zero,
                      int max_value, const char* flag, Error& error) {
    if (s.empty()) {
        error = {ErrorKind::Config,
                 std::string(flag) + " requires a numeric value"};
        return false;
    }
    errno = 0;
    char* end = nullptr;
    long v = std::strtol(s.c_str(), &end, 10);
    if (errno != 0 || end == s.c_str() || *end != '\0') {
        error = {ErrorKind::Config,
                 std::string(flag) + ": not an integer: " + s};
        return false;
    }
    long min_value = allow_zero ? 0L : 1L;
    if (v < min_value || v > max_value) {
        error = {ErrorKind::Config,
                 std::string(flag) + ": value out of range ["};
        error.message += std::to_string(min_value) + ", " +
                         std::to_string(max_value) + "]: " + s;
        return false;
    }
    out = static_cast<int>(v);
    return true;
}

bool parseScale(const std::string& s, double& out, Error& error) {
    errno = 0;
    char* end = nullptr;
    double v = std::strtod(s.c_str(), &end);
    if (errno != 0 || end == s.c_str() || *end != '\0' ||
        !(v == v) || v == std::numeric_limits<double>::infinity() ||
        v == -std::numeric_limits<double>::infinity()) {
        error = {ErrorKind::Config, "--scale: not a finite number: " + s};
        return false;
    }
    out = v;
    return true;
}

bool needValue(int i, int argc, const char* flag, Error& error) {
    if (i + 1 >= argc) {
        error = {ErrorKind::Config,
                 std::string(flag) + " requires a value"};
        return false;
    }
    return true;
}

}  // namespace

bool parseArgs(int argc, char** argv, Config& cfg, Error& error) {
    for (int i = 1; i < argc; ++i) {
        const std::string flag = argv[i];
        auto value = [&](std::string& dst) -> bool {
            if (!needValue(i, argc, flag.c_str(), error)) return false;
            dst = argv[++i];
            return true;
        };
        if (flag == "--font") {
            if (!value(cfg.font_path)) return false;
        } else if (flag == "--input") {
            if (!value(cfg.input_path)) return false;
        } else if (flag == "--json-out") {
            if (!value(cfg.json_out)) return false;
        } else if (flag == "--make-fixture") {
            if (!value(cfg.make_fixture)) return false;
        } else if (flag == "--width") {
            if (!needValue(i, argc, flag.c_str(), error)) return false;
            if (!parsePositiveInt(argv[++i], cfg.width, false,
                                  kMaxCanvasPx, "--width", error))
                return false;
        } else if (flag == "--height") {
            if (!needValue(i, argc, flag.c_str(), error)) return false;
            if (!parsePositiveInt(argv[++i], cfg.height, true,
                                  kMaxCanvasPx, "--height", error))
                return false;
        } else if (flag == "--safe-left") {
            if (!needValue(i, argc, flag.c_str(), error)) return false;
            if (!parsePositiveInt(argv[++i], cfg.safe_left, true,
                                  kMaxCanvasPx, "--safe-left", error))
                return false;
        } else if (flag == "--safe-right") {
            if (!needValue(i, argc, flag.c_str(), error)) return false;
            if (!parsePositiveInt(argv[++i], cfg.safe_right, true,
                                  kMaxCanvasPx, "--safe-right", error))
                return false;
        } else if (flag == "--safe-top") {
            if (!needValue(i, argc, flag.c_str(), error)) return false;
            if (!parsePositiveInt(argv[++i], cfg.safe_top, true,
                                  kMaxCanvasPx, "--safe-top", error))
                return false;
        } else if (flag == "--safe-bottom") {
            if (!needValue(i, argc, flag.c_str(), error)) return false;
            if (!parsePositiveInt(argv[++i], cfg.safe_bottom, true,
                                  kMaxCanvasPx, "--safe-bottom", error))
                return false;
        } else if (flag == "--font-size") {
            if (!needValue(i, argc, flag.c_str(), error)) return false;
            if (!parsePositiveInt(argv[++i], cfg.font_size_px, false,
                                  kMaxFontSizePx, "--font-size", error))
                return false;
        } else if (flag == "--scale") {
            if (!needValue(i, argc, flag.c_str(), error)) return false;
            if (!parseScale(argv[++i], cfg.scale, error)) return false;
        } else if (flag == "--wrap") {
            if (!needValue(i, argc, flag.c_str(), error)) return false;
            if (!parseWrapPolicy(argv[++i], cfg.wrap)) {
                error = {ErrorKind::Config,
                         "--wrap must be one of space, punctuation, hard"};
                return false;
            }
        } else {
            error = {ErrorKind::Config, "unknown argument: " + flag};
            return false;
        }
    }
    return true;
}

bool validateConfig(const Config& cfg, Error& error) {
    if (!cfg.make_fixture.empty()) {
        // Fixture generation must not be mixed with an analysis run.
        if (!cfg.font_path.empty() || !cfg.input_path.empty()) {
            error = {ErrorKind::Config,
                     "--make-fixture cannot be combined with --font/--input"};
            return false;
        }
        return true;
    }
    if (cfg.font_path.empty()) {
        error = {ErrorKind::Config, "missing required --font"};
        return false;
    }
    if (cfg.input_path.empty()) {
        error = {ErrorKind::Config, "missing required --input"};
        return false;
    }
    if (cfg.width <= 0) {
        error = {ErrorKind::Config, "missing required --width"};
        return false;
    }
    if (cfg.font_size_px <= 0) {
        error = {ErrorKind::Config, "missing required --font-size"};
        return false;
    }
    if (cfg.scale < kMinScale || cfg.scale > kMaxScale) {
        error = {ErrorKind::Config,
                 "--scale out of range [" + std::to_string(kMinScale) +
                     ", " + std::to_string(kMaxScale) + "]"};
        return false;
    }
    if (cfg.safe_left + cfg.safe_right >= cfg.width) {
        error = {ErrorKind::Config,
                 "horizontal safe area leaves no content width"};
        return false;
    }
    if (cfg.height > 0 &&
        cfg.safe_top + cfg.safe_bottom >= cfg.height) {
        error = {ErrorKind::Config,
                 "vertical safe area leaves no content height"};
        return false;
    }
    return true;
}

}  // namespace sm
