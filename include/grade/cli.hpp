#ifndef GRADE_CLI_HPP
#define GRADE_CLI_HPP

#include <string>

namespace grade {

enum class Command { none, fixture, render };
enum class ColorSpaceId { srgb, displayP3 };
enum class InterpId { trilinear, tetrahedral };
enum class OobId { clamp, error };
enum class PathMode { single, compare };

struct FixtureOptions {
    std::string dir = "build/demo";
    int width = 96;
    int height = 64;
    int grid = 17;
};

struct RenderOptions {
    std::string input;
    std::string lut;
    std::string output;
    ColorSpaceId inputSpace = ColorSpaceId::srgb;
    ColorSpaceId lutSpace = ColorSpaceId::srgb;
    ColorSpaceId outputSpace = ColorSpaceId::srgb;
    InterpId interpolation = InterpId::trilinear;
    OobId outOfBounds = OobId::clamp;
    PathMode path = PathMode::single;
    int threads = 1;
};

struct Arguments {
    Command command = Command::none;
    FixtureOptions fixture;
    RenderOptions render;
};

// Parses argv (without the program name). Throws UsageError on any
// unknown option, missing value, or invalid value.
Arguments parseCommandLine(int argc, char** argv);

// Rejects path traversal ("..") and empty paths. Absolute paths are
// allowed; the tool only writes to the exact paths the user names.
void validatePath(const std::string& path);

const char* toString(ColorSpaceId id);
const char* toString(InterpId id);
const char* toString(PathMode mode);

std::string usageText();

} // namespace grade

#endif
