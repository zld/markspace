#include "cli.hpp"

#include <cstdlib>
#include <string>

namespace tl {

namespace {

bool parse_positive_int(const std::string& text, int* value) {
    if (text.empty()) return false;
    for (char c : text) {
        if (c < '0' || c > '9') return false;
    }
    const long long parsed = std::strtoll(text.c_str(), nullptr, 10);
    if (parsed <= 0 || parsed > 2'000'000'000LL) return false;
    *value = static_cast<int>(parsed);
    return true;
}

bool parse_seed(const std::string& text, std::uint64_t* seed) {
    if (text.empty()) return false;
    try {
        std::size_t consumed = 0;
        *seed = std::stoull(text, &consumed, 10);
        return consumed == text.size();
    } catch (...) {
        return false;
    }
}

WipeDirection parse_direction(const std::string& text, bool* ok) {
    *ok = true;
    if (text == "ltr") return WipeDirection::LeftToRight;
    if (text == "rtl") return WipeDirection::RightToLeft;
    if (text == "ttb") return WipeDirection::TopToBottom;
    if (text == "btt") return WipeDirection::BottomToTop;
    *ok = false;
    return WipeDirection::LeftToRight;
}

Options arg_error(const std::string& message) {
    Options opts;
    opts.valid = false;
    opts.error = message;
    return opts;
}

}  // namespace

const char* wipe_direction_name(WipeDirection direction) {
    switch (direction) {
        case WipeDirection::LeftToRight: return "ltr";
        case WipeDirection::RightToLeft: return "rtl";
        case WipeDirection::TopToBottom: return "ttb";
        case WipeDirection::BottomToTop: return "btt";
    }
    return "ltr";
}

Options parse_args(int argc, char** argv) {
    Options opts;
    if (argc < 2) {
        opts.show_help = true;
        return opts;
    }
    const std::string first = argv[1];
    if (first == "-h" || first == "--help") {
        opts.show_help = true;
        return opts;
    }
    if (first != "demo") return arg_error("unknown command '" + first + "'");
    opts.command = "demo";
    DemoConfig& cfg = opts.demo;

    for (int i = 2; i < argc; ++i) {
        const std::string flag = argv[i];
        auto need_value = [&]() -> std::string {
            if (i + 1 >= argc) {
                opts.error = "missing value for " + flag;
                opts.valid = false;
                return "";
            }
            return argv[++i];
        };
        if (!opts.valid) break;

        if (flag == "--out") {
            cfg.out_dir = need_value();
        } else if (flag == "--fps") {
            if (!parse_positive_int(need_value(), &cfg.fps))
                return arg_error("--fps needs a positive integer");
        } else if (flag == "--frames") {
            if (!parse_positive_int(need_value(), &cfg.frames))
                return arg_error("--frames needs a positive integer");
        } else if (flag == "--width") {
            if (!parse_positive_int(need_value(), &cfg.width))
                return arg_error("--width needs a positive integer");
        } else if (flag == "--height") {
            if (!parse_positive_int(need_value(), &cfg.height))
                return arg_error("--height needs a positive integer");
        } else if (flag == "--seed") {
            if (!parse_seed(need_value(), &cfg.seed))
                return arg_error("--seed needs an unsigned integer");
        } else if (flag == "--speed") {
            cfg.speed_tokens.push_back(need_value());
        } else if (flag == "--wipe") {
            bool ok = false;
            cfg.wipe_direction = parse_direction(need_value(), &ok);
            if (!ok) return arg_error("--wipe expects ltr, rtl, ttb or btt");
        } else if (flag == "--verify") {
            cfg.verify = true;
        } else {
            return arg_error("unknown argument '" + flag + "'");
        }
    }

    if (opts.valid && cfg.out_dir.empty())
        return arg_error("--out is required");
    if (opts.valid && !valid_dimensions(cfg.width, cfg.height))
        return arg_error("frame dimensions outside supported limits");
    if (opts.valid && (cfg.frames <= 0 || cfg.frames > kMaxFrames))
        return arg_error("frame count outside [1," +
                         std::to_string(kMaxFrames) + "]");
    if (opts.valid && (cfg.fps < 1 || cfg.fps > 240))
        return arg_error("fps outside [1,240]");
    return opts;
}

bool build_speed_map(const DemoConfig& config, SpeedMap* map,
                     std::string* error) {
    std::vector<SpeedNode> nodes;
    if (config.speed_tokens.empty()) {
        nodes.push_back(SpeedNode{0, Rate{1, 1}});
    } else {
        for (const std::string& token : config.speed_tokens) {
            SpeedNode node;
            if (!parse_rate_spec(token, &node, error)) return false;
            nodes.push_back(node);
        }
    }
    return map->build(std::move(nodes), config.frames - 1, error);
}

}  // namespace tl
