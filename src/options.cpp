#include "color/demo.hpp"

#include <cerrno>
#include <cstdlib>
#include <limits>
#include <stdexcept>

namespace color {

namespace {

std::size_t parseCount(const std::string& text, const char* option) {
    if (text.empty()) throw std::invalid_argument(std::string(option) + " requires a value");
    for (char ch : text) {
        if (ch < '0' || ch > '9') {
            throw std::invalid_argument(std::string(option) + " must be a non-negative integer: " +
                                        text);
        }
    }
    errno = 0;
    unsigned long long value = std::strtoull(text.c_str(), nullptr, 10);
    if (errno == ERANGE || value > std::numeric_limits<std::size_t>::max()) {
        throw std::invalid_argument(std::string(option) + " is out of range: " + text);
    }
    return static_cast<std::size_t>(value);
}

std::string requireValue(int argc, char** argv, int& i, const std::string& option) {
    if (i + 1 >= argc) throw std::invalid_argument(option + " requires a value");
    return argv[++i];
}

int parseSize(const std::string& text) {
    const std::size_t value = parseCount(text, "--size");
    if (value < static_cast<std::size_t>(kMinLutSize) ||
        value > static_cast<std::size_t>(kMaxLutSize)) {
        throw std::invalid_argument("--size must be in [2,65], got " + text);
    }
    return static_cast<int>(value);
}

std::size_t parsePixels(const std::string& text) {
    const std::size_t value = parseCount(text, "--pixels");
    if (value < 1 || value > kMaxPixels) {
        throw std::invalid_argument("--pixels must be in [1,16777216], got " + text);
    }
    return value;
}

int parseThreads(const std::string& text) {
    const std::size_t value = parseCount(text, "--threads");
    if (value < 1 || value > static_cast<std::size_t>(kMaxThreads)) {
        throw std::invalid_argument("--threads must be in [1,32], got " + text);
    }
    return static_cast<int>(value);
}

std::uint64_t parseSeed(const std::string& text) {
    if (text.empty()) throw std::invalid_argument("--seed requires a value");
    // strtoull silently wraps a leading minus sign; a negative seed is a typo.
    if (text.find('-') != std::string::npos) {
        throw std::invalid_argument("--seed must be a non-negative integer: " + text);
    }
    errno = 0;
    char* end = nullptr;
    const unsigned long long value = std::strtoull(text.c_str(), &end, 10);
    if (end != text.c_str() + text.size() || errno == ERANGE) {
        throw std::invalid_argument("--seed must be an unsigned 64-bit integer: " + text);
    }
    return static_cast<std::uint64_t>(value);
}

BoundaryPolicy parseBoundary(const std::string& text) {
    if (text == "clamp") return BoundaryPolicy::Clamp;
    if (text == "reject") return BoundaryPolicy::Reject;
    throw std::invalid_argument("--boundary must be clamp or reject: " + text);
}

OutputFormat parseFormat(const std::string& text) {
    if (text == "json") return OutputFormat::Json;
    if (text == "text") return OutputFormat::Text;
    throw std::invalid_argument("--format must be json or text: " + text);
}

}  // namespace

Options parseOptions(int argc, char** argv) {
    Options options;
    if (argc < 2) {
        throw std::invalid_argument("missing command (try --help)");
    }
    int index = 1;
    if (std::string(argv[index]) == "--help" || std::string(argv[index]) == "-h") {
        options.requestHelp = true;
        return options;
    }
    if (argv[index][0] == '-') {
        throw std::invalid_argument(std::string("expected a command, got option ") + argv[index]);
    }
    options.command = argv[index];
    if (options.command != "demo") {
        throw std::invalid_argument("unknown command: " + options.command);
    }

    for (++index; index < argc; ++index) {
        const std::string arg = argv[index];
        if (arg == "--help" || arg == "-h") {
            options.requestHelp = true;
        } else if (arg == "--size") {
            options.size = parseSize(requireValue(argc, argv, index, arg));
        } else if (arg == "--pixels") {
            options.pixels = parsePixels(requireValue(argc, argv, index, arg));
        } else if (arg == "--threads") {
            options.threads = parseThreads(requireValue(argc, argv, index, arg));
        } else if (arg == "--input-space") {
            options.inputSpace = parseColorSpace(requireValue(argc, argv, index, arg));
        } else if (arg == "--lut-space") {
            options.lutSpace = parseColorSpace(requireValue(argc, argv, index, arg));
        } else if (arg == "--boundary") {
            options.boundary = parseBoundary(requireValue(argc, argv, index, arg));
        } else if (arg == "--format") {
            options.format = parseFormat(requireValue(argc, argv, index, arg));
        } else if (arg == "--seed") {
            options.seed = parseSeed(requireValue(argc, argv, index, arg));
        } else if (arg == "--output") {
            options.outputPath = requireValue(argc, argv, index, arg);
        } else if (arg == "--lut-file") {
            options.lutFile = requireValue(argc, argv, index, arg);
        } else if (arg == "--lut-order") {
            options.lutOrder = requireValue(argc, argv, index, arg);
            if (options.lutOrder != "rgb" && options.lutOrder != "bgr") {
                throw std::invalid_argument("--lut-order must be rgb or bgr: " +
                                            options.lutOrder);
            }
        } else {
            throw std::invalid_argument("unknown option: " + arg);
        }
    }
    if (options.requestHelp) return options;
    if (options.inputSpace == ColorSpace::Xyz) {
        throw std::invalid_argument("--input-space xyz is not supported: sampled pixels are "
                                    "RGB image data; use srgb or linear-rgb");
    }
    if (!options.lutFile.empty() && options.lutOrder != "rgb") {
        throw std::invalid_argument("--lut-order only applies to generated LUTs");
    }
    return options;
}

std::string usage() {
    return "lutcheck - offline 3D LUT and colour-space verification\n"
           "\n"
           "Usage:\n"
           "  lutcheck demo [options]\n"
           "\n"
           "Options:\n"
           "  --size N             generated LUT grid size, 2..65 (default 17)\n"
           "  --pixels N           sample pixel count, 1..16777216 (default 65536)\n"
           "  --input-space NAME   srgb | linear-rgb (default srgb); xyz is a\n"
           "                       processing space, not a sampled image space\n"
           "  --lut-space NAME     srgb | linear-rgb | xyz (default linear-rgb)\n"
           "  --threads N          parallel worker count, 1..32 (default 4)\n"
           "  --boundary POLICY    clamp | reject (default clamp)\n"
           "  --format FORMAT      json | text (default text)\n"
           "  --seed N             PRNG seed (default 11400714819323198485)\n"
           "  --output PATH        also write the report to PATH\n"
           "  --lut-file PATH      use an external .cube instead of a generated LUT\n"
           "  --lut-order ORDER    rgb | bgr for the generated .cube (default rgb)\n"
           "  -h, --help           show this help\n"
           "\n"
           "Exit codes: 0 success, 2 bad arguments, 3 invalid LUT/colour data,\n"
           "            4 processing or output failure.\n";
}

}  // namespace color
