#ifndef LUT_IO_UTIL_HPP
#define LUT_IO_UTIL_HPP

#include <string>

#include "lut/model.hpp"

namespace lut {

// Reads a whole binary file. Throws IoError when it cannot be opened.
std::string readBinaryFile(const std::string& path);

// Writes bytes to a sibling temporary file, fsyncs, then atomically renames
// over `path`. Any failure removes the temporary file and throws IoError, so
// a partial target can never appear as a complete output.
void writeFileAtomic(const std::string& path, const std::string& bytes);

}  // namespace lut

#endif  // LUT_IO_UTIL_HPP
