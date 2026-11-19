// cube_parser.hpp - .cube LUT parsing interface.
#pragma once

#include <stdexcept>
#include <string>

#include "color_types.hpp"

namespace lutcheck {

// Parse error carrying the 1-based source line that caused it
// (0 means the error is not tied to a single line).
class ParseError : public std::runtime_error {
 public:
  ParseError(int line, const std::string& message);
  int line() const { return line_; }

 private:
  int line_;
};

// File read/write failure.
class IoError : public std::runtime_error {
 public:
  explicit IoError(const std::string& message) : std::runtime_error(message) {}
};

// Parse .cube text into a Lut3D. Throws ParseError with a line number on
// malformed input and std::bad_alloc on allocation failure.
Lut3D parse_cube_text(const std::string& text);

// Read a file and parse it. Throws IoError when the file cannot be read.
Lut3D parse_cube_file(const std::string& path);

}  // namespace lutcheck
