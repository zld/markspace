#ifndef LUT_CUBE_READER_HPP
#define LUT_CUBE_READER_HPP

#include <string>

#include "lut/model.hpp"

namespace lut {

// Parses an Adobe .cube file. Throws ParseError (with line numbers) for
// duplicate/missing/unsupported directives, bad node counts or non-finite
// values, and IoError when the file cannot be opened.
Lut3D readCube(const std::string& path);

// Writes a .cube file directly to `path` (callers needing atomic replacement
// should use writeFileAtomic). Throws IoError on failure.
void writeCube(const std::string& path, const Lut3D& lut);

// Renders the .cube text representation (used by tests and the writer).
std::string renderCube(const Lut3D& lut);

}  // namespace lut

#endif  // LUT_CUBE_READER_HPP
