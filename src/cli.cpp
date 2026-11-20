#include "grade/cli.hpp"

#include <sstream>

#include "grade/error.hpp"

namespace grade {

namespace {

// Splits a filesystem path on '/' and rejects any ".." segment so the
// tool can never be tricked into writing outside the named target.
void checkSegments(const std::string& path) {
    if (path.empty()) {
        throw UsageError("path must not be empty");
    }
    size_t start = 0;
    while (start <= path.size()) {
        size_t slash = path.find('/', start);
        std::string segment = path.substr(
            start, slash == std::string::npos ? std::string::npos
                                              : slash - start);
        if (segment == "..") {
            throw UsageError("path traversal ('..') is not allowed: " + path);
        }
        if (slash == std::string::npos) {
            break;
        }
        start = slash + 1;
    }
}

[[maybe_unused]] int parsePositiveInt(const std::string& value,
                                      const std::string& flag, int minValue,
                                      int maxValue) {
    try {
        size_t consumed = 0;
        int parsed = std::stoi(value, &consumed, 10);
        if (consumed != value.size()) {
            throw UsageError(flag + " expects an integer, got '" + value + "'");
        }
        if (parsed < minValue || parsed > maxValue) {
            std::ostringstream oss;
            oss << flag << " must be in [" << minValue << ", " << maxValue
                << "], got " << parsed;
            throw UsageError(oss.str());
        }
        return parsed;
    } catch (const std::invalid_argument&) {
        throw UsageError(flag + " expects an integer, got '" + value + "'");
    } catch (const std::out_of_range&) {
        throw UsageError(flag + " value out of range: " + value);
    }
}

} // namespace

void validatePath(const std::string& path) { checkSegments(path); }

const char* toString(ColorSpaceId id) {
    switch (id) {
        case ColorSpaceId::srgb:
            return "srgb";
        case ColorSpaceId::displayP3:
            return "display-p3";
    }
    return "unknown";
}

const char* toString(InterpId id) {
    switch (id) {
        case InterpId::trilinear:
            return "trilinear";
        case InterpId::tetrahedral:
            return "tetrahedral";
    }
    return "unknown";
}

const char* toString(PathMode mode) {
    return mode == PathMode::compare ? "compare" : "single";
}

// Full option parsing is added with the CLI batch; the scaffolding only
// needs to recognise the command name so the program links and runs.
Arguments parseCommandLine(int argc, char** argv) {
    Arguments args;
    if (argc < 1) {
        throw UsageError("missing command; expected 'fixture' or 'render'");
    }
    std::string command = argv[0];
    if (command == "fixture") {
        args.command = Command::fixture;
    } else if (command == "render") {
        args.command = Command::render;
    } else {
        throw UsageError("unknown command '" + command +
                         "'; expected 'fixture' or 'render'");
    }
    return args;
}

std::string usageText() {
    return R"(lutgrade - deterministic LUT grading for P6 PPM images

Usage:
  lutgrade fixture --dir DIR --width W --height H --grid N
  lutgrade render --input PPM --lut CUBE --output PPM
                   --input-space srgb|display-p3
                   --lut-space   srgb|display-p3
                   --output-space srgb|display-p3
                   [--interpolation trilinear|tetrahedral]
                   [--out-of-bounds clamp|error]
                   [--path single|compare] [--threads N]

Exit codes: 0 success, 2 usage error, 3 invalid file content,
            4 file I/O failure, 5 rendering failure.
)";
}

} // namespace grade
