#pragma once

#include <istream>
#include <string>

#include "color/types.hpp"

namespace color {

class CubeError : public LutError {
public:
    CubeError(const std::string& source, std::size_t line, const std::string& message);
    const std::string& source() const { return source_; }
    std::size_t line() const { return line_; }
    const std::string& detail() const { return detail_; }

private:
    std::string source_;
    std::size_t line_;
    std::string detail_;
};

// Parses an Adobe .cube 3D LUT. Supported statements: TITLE,
// LUT_3D_SIZE (one cubic size or "nx ny nz"), DOMAIN_MIN, DOMAIN_MAX and
// the optional LUT_3D_ORDER extension (RGB or BGR). Comments start with '#'
// and blank lines are ignored. Every rejection is reported with a line number.
LUT parseCube(std::istream& input, const std::string& source = "<stream>");
LUT parseCubeText(const std::string& text, const std::string& source = "<memory>");

// Serialises a LUT using red-fastest sample order. The output is parseable by
// parseCube and is used to exercise the parser in the demo path.
std::string serializeCube(const LUT& lut, const std::string& title = {});

}  // namespace color
