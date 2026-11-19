#pragma once

#include <cstddef>
#include <cstdint>
#include <string>
#include <vector>

#include "color/types.hpp"

namespace color {

enum class OutputFormat { Json, Text };

struct Options {
    std::string command = "demo";
    int size = 17;
    std::size_t pixels = 65536;
    ColorSpace inputSpace = ColorSpace::Srgb;
    ColorSpace lutSpace = ColorSpace::LinearRgb;
    int threads = 4;
    BoundaryPolicy boundary = BoundaryPolicy::Clamp;
    OutputFormat format = OutputFormat::Text;
    std::uint64_t seed = 0x9E3779B97F4A7C15ULL;
    std::string outputPath;
    std::string lutFile;
    std::string lutOrder = "rgb";
    bool requestHelp = false;
};

// Parses the command line. Throws std::invalid_argument for any illegal
// parameter; `--help` sets requestHelp rather than throwing.
Options parseOptions(int argc, char** argv);
std::string usage();

// Deterministic demo data. Both helpers only use their arguments, so repeated
// runs with the same options generate identical bytes.
LUT makeDemoLut(int size);
std::vector<RGB> makeDemoPixels(std::size_t count, ColorSpace inputSpace, std::uint64_t seed);

// Produces .cube text for a LUT; `order` is "rgb" or "bgr" and only changes
// the on-disk channel order plus the LUT_3D_ORDER statement.
std::string cubeTextWithOrder(const LUT& lut, const std::string& order);

}  // namespace color
