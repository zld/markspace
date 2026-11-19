#include <ass/ass.h>

#include <algorithm>
#include <atomic>
#include <cmath>
#include <cstdio>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <iostream>
#include <map>
#include <optional>
#include <string>
#include <thread>
#include <vector>

#include "ass_document.hpp"
#include "inline_tags.hpp"
#include "layout_probe.hpp"
#include "report.hpp"
#include "style_resolver.hpp"

namespace fs = std::filesystem;

namespace {

constexpr int kExitOk = 0;
constexpr int kExitInput = 1;
constexpr int kExitPartialRender = 2;
constexpr int kExitReportWrite = 3;

struct Options {
    std::string input;
    std::string json_path;
    std::string font_dir;
    sp::Canvas canvas;
    int jobs = 1;
    bool strict = false;
    bool font_dir_given = false;
};

void print_usage() {
    std::printf(
        "usage: subtitle_probe --input FILE [--canvas WxH] [--font-dir DIR] [--json FILE]\n"
        "                      [--jobs N] [--strict]\n"
        "\n"
        "  --input FILE     ASS script to inspect (required)\n"
        "  --canvas WxH     off-screen frame size (default 1920x1080)\n"
        "  --font-dir DIR   directory scanned for fonts (libass provider: none)\n"
        "  --json FILE      write the full report as JSON (atomic replace)\n"
        "  --jobs N         render worker threads (output order is preserved)\n"
        "  --strict         treat input and style diagnostics as fatal errors\n"
        "\n"
        "exit codes: 0 all ok, 1 input/argument error, 2 some events failed to render,\n"
        "            3 report could not be written\n");
}

std::string sanitize_path(const std::string& path) { return sp::sanitize_for_output(path, 300); }

// libass recursively opens every entry under the font directory. A FIFO or
// device node would block that scan indefinitely, so reject such trees up
// front instead of hanging the whole run.
bool validate_font_dir_contents(const std::string& path, std::string& error) {
    std::error_code ec;
    size_t entries = 0;
    fs::recursive_directory_iterator it(path, fs::directory_options::skip_permission_denied, ec);
    if (ec) {
        error = "cannot scan --font-dir: " + ec.message();
        return false;
    }
    for (fs::recursive_directory_iterator end; it != end; it.increment(ec)) {
        if (ec) break;
        if (++entries > 20000) {
            error = "--font-dir contains more than 20000 entries; point it at a font directory";
            return false;
        }
        const fs::file_status link_status = it->symlink_status(ec);
        if (ec) { ec.clear(); continue; }
        if (fs::is_directory(link_status) || fs::is_regular_file(link_status)) continue;
        if (fs::is_symlink(link_status)) {
            const fs::file_status target = fs::status(it->path(), ec);
            if (!ec && fs::is_regular_file(target)) continue;
        }
        error = "--font-dir contains a non-regular file (FIFO, socket or device) at '" +
                sanitize_path(it->path().string()) + "'";
        return false;
    }
    return true;
}

bool parse_canvas(const std::string& text, sp::Canvas& out, std::string& error) {
    size_t x = text.find('x');
    if (x == std::string::npos) x = text.find('X');
    if (x == std::string::npos) {
        error = "--canvas expects WIDTHxHEIGHT, got '" + sanitize_path(text) + "'";
        return false;
    }
    try {
        size_t used = 0;
        long w = std::stol(text.substr(0, x), &used);
        if (used != x) throw std::invalid_argument("width");
        size_t used2 = 0;
        long h = std::stol(text.substr(x + 1), &used2);
        if (used2 != text.size() - x - 1) throw std::invalid_argument("height");
        if (w <= 0 || h <= 0 || w > 32768 || h > 32768) {
            error = "--canvas dimensions out of range: " + sanitize_path(text);
            return false;
        }
        out.width = static_cast<int>(w);
        out.height = static_cast<int>(h);
        return true;
    } catch (...) {
        error = "--canvas expects WIDTHxHEIGHT, got '" + sanitize_path(text) + "'";
        return false;
    }
}

bool parse_jobs(const std::string& text, int& out, std::string& error) {
    try {
        size_t used = 0;
        long n = std::stol(text, &used);
        if (used != text.size() || n < 1 || n > 256) throw std::invalid_argument("jobs");
        out = static_cast<int>(n);
        return true;
    } catch (...) {
        error = "--jobs expects an integer in [1,256], got '" + sanitize_path(text) + "'";
        return false;
    }
}

bool parse_args(int argc, char** argv, Options& options, std::string& error) {
    for (int i = 1; i < argc; ++i) {
        std::string arg = argv[i];
        auto next = [&](const char* name) -> std::optional<std::string> {
            if (i + 1 >= argc) {
                error = std::string("missing value for ") + name;
                return std::nullopt;
            }
            return std::string(argv[++i]);
        };
        if (arg == "--input" || arg == "-i") {
            auto v = next("--input"); if (!v) return false; options.input = *v;
        } else if (arg == "--json") {
            auto v = next("--json"); if (!v) return false; options.json_path = *v;
        } else if (arg == "--font-dir") {
            auto v = next("--font-dir"); if (!v) return false;
            options.font_dir = *v;
            options.font_dir_given = true;
        } else if (arg == "--canvas") {
            auto v = next("--canvas"); if (!v) return false;
            if (!parse_canvas(*v, options.canvas, error)) return false;
        } else if (arg == "--jobs" || arg == "-j") {
            auto v = next("--jobs"); if (!v) return false;
            if (!parse_jobs(*v, options.jobs, error)) return false;
        } else if (arg == "--strict") {
            options.strict = true;
        } else if (arg == "--help" || arg == "-h") {
            print_usage();
            std::exit(kExitOk);
        } else if (arg.rfind("--canvas=", 0) == 0) {
            if (!parse_canvas(arg.substr(9), options.canvas, error)) return false;
        } else if (arg.rfind("--input=", 0) == 0) {
            options.input = arg.substr(8);
        } else if (arg.rfind("--json=", 0) == 0) {
            options.json_path = arg.substr(7);
        } else if (arg.rfind("--jobs=", 0) == 0) {
            if (!parse_jobs(arg.substr(7), options.jobs, error)) return false;
        } else if (arg.rfind("--font-dir=", 0) == 0) {
            options.font_dir = arg.substr(11);
            options.font_dir_given = true;
        } else {
            error = "unknown argument '" + sanitize_path(arg) + "'";
            return false;
        }
    }
    if (options.input.empty()) {
        error = "--input is required";
        return false;
    }
    return true;
}

std::string libass_version_string() {
    int version = ass_library_version();
    char buf[32];
    std::snprintf(buf, sizeof buf, "%d.%d.%d", (version >> 24) & 0xFF, (version >> 16) & 0xFF,
                  (version >> 8) & 0xFF);
    return buf;
}

}  // namespace

int main(int argc, char** argv) {
    Options options;
    std::string error;
    if (!parse_args(argc, argv, options, error)) {
        std::fprintf(stderr, "error: %s\n\n", error.c_str());
        print_usage();
        return kExitInput;
    }
    std::fprintf(stderr, "error: run pipeline is not implemented yet\n");
    return kExitInput;
}
