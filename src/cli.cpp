#include "grade/cli.hpp"

#include <sstream>

#include "grade/error.hpp"

namespace grade {

namespace {

constexpr int kMaxDimension = 32767;
constexpr int kMaxGrid = 65;
constexpr int kMinGrid = 2;
constexpr int kMaxThreads = 256;

// Splits a filesystem path on '/' and rejects any ".." segment.
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

int parseInt(const std::string& value, const std::string& flag, int lo,
             int hi) {
    try {
        size_t consumed = 0;
        int parsed = std::stoi(value, &consumed, 10);
        if (consumed != value.size()) {
            throw UsageError(flag + " expects an integer, got '" + value + "'");
        }
        if (parsed < lo || parsed > hi) {
            std::ostringstream oss;
            oss << flag << " must be in [" << lo << ", " << hi << "], got "
                << parsed;
            throw UsageError(oss.str());
        }
        return parsed;
    } catch (const std::invalid_argument&) {
        throw UsageError(flag + " expects an integer, got '" + value + "'");
    } catch (const std::out_of_range&) {
        throw UsageError(flag + " value out of range: " + value);
    }
}

ColorSpaceId parseSpace(const std::string& value, const std::string& flag) {
    if (value == "srgb") {
        return ColorSpaceId::srgb;
    }
    if (value == "display-p3") {
        return ColorSpaceId::displayP3;
    }
    throw UsageError(flag + " must be 'srgb' or 'display-p3', got '" + value +
                     "'");
}

InterpId parseInterp(const std::string& value) {
    if (value == "trilinear") {
        return InterpId::trilinear;
    }
    if (value == "tetrahedral") {
        return InterpId::tetrahedral;
    }
    throw UsageError(
        "--interpolation must be 'trilinear' or 'tetrahedral', got '" + value +
        "'");
}

OobId parseOob(const std::string& value) {
    if (value == "clamp") {
        return OobId::clamp;
    }
    if (value == "error") {
        return OobId::error;
    }
    throw UsageError("--out-of-bounds must be 'clamp' or 'error', got '" +
                     value + "'");
}

PathMode parsePathMode(const std::string& value) {
    if (value == "single") {
        return PathMode::single;
    }
    if (value == "compare") {
        return PathMode::compare;
    }
    throw UsageError("--path must be 'single' or 'compare', got '" + value +
                     "'");
}

// Consumes one "--flag value" pair and reports unknown flags.
std::string nextValue(int argc, char** argv, int& index,
                      const std::string& flag) {
    if (index + 1 >= argc) {
        throw UsageError(flag + " requires a value");
    }
    return argv[++index];
}

void parseFixture(int argc, char** argv, FixtureOptions& opts) {
    for (int i = 1; i < argc; ++i) {
        std::string flag = argv[i];
        std::string value = nextValue(argc, argv, i, flag);
        if (flag == "--dir") {
            checkSegments(value);
            opts.dir = value;
        } else if (flag == "--width") {
            opts.width = parseInt(value, flag, 1, kMaxDimension);
        } else if (flag == "--height") {
            opts.height = parseInt(value, flag, 1, kMaxDimension);
        } else if (flag == "--grid") {
            opts.grid = parseInt(value, flag, kMinGrid, kMaxGrid);
        } else {
            throw UsageError("fixture: unknown option '" + flag + "'");
        }
    }
}

void requireSet(const std::string& value, const std::string& flag) {
    if (value.empty()) {
        throw UsageError("render: missing required option " + flag);
    }
}

void parseRender(int argc, char** argv, RenderOptions& opts) {
    for (int i = 1; i < argc; ++i) {
        std::string flag = argv[i];
        std::string value = nextValue(argc, argv, i, flag);
        if (flag == "--input") {
            checkSegments(value);
            opts.input = value;
        } else if (flag == "--lut") {
            checkSegments(value);
            opts.lut = value;
        } else if (flag == "--output") {
            checkSegments(value);
            opts.output = value;
        } else if (flag == "--input-space") {
            opts.inputSpace = parseSpace(value, flag);
        } else if (flag == "--lut-space") {
            opts.lutSpace = parseSpace(value, flag);
        } else if (flag == "--output-space") {
            opts.outputSpace = parseSpace(value, flag);
        } else if (flag == "--interpolation") {
            opts.interpolation = parseInterp(value);
        } else if (flag == "--out-of-bounds") {
            opts.outOfBounds = parseOob(value);
        } else if (flag == "--path") {
            opts.path = parsePathMode(value);
        } else if (flag == "--threads") {
            opts.threads = parseInt(value, flag, 1, kMaxThreads);
        } else {
            throw UsageError("render: unknown option '" + flag + "'");
        }
    }
    requireSet(opts.input, "--input");
    requireSet(opts.lut, "--lut");
    requireSet(opts.output, "--output");
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

Arguments parseCommandLine(int argc, char** argv) {
    Arguments args;
    if (argc < 1) {
        throw UsageError("missing command; expected 'fixture' or 'render'");
    }
    std::string command = argv[0];
    if (command == "fixture") {
        args.command = Command::fixture;
        parseFixture(argc, argv, args.fixture);
    } else if (command == "render") {
        args.command = Command::render;
        parseRender(argc, argv, args.render);
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
                   [--out-of-bounds clamp|error] [--path single|compare]
                   [--threads N]

Exit codes: 0 success, 2 usage error, 3 invalid file content,
            4 file I/O failure, 5 rendering failure.
)";
}

} // namespace grade
