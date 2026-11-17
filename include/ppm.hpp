#pragma once

#include <string>

#include "frame_buffer.hpp"

namespace tl {

// Writes fb as binary P6 PPM. Atomic: writes "<path>.tmp" first, then
// renames over the target. On any failure the temp file is removed.
// Throws IoError on write/rename failure.
void write_ppm_atomic(const std::string& path, const FrameBuffer& fb);

// Strict P6 reader. Throws IoError if the file cannot be opened/read;
// throws DataError on a malformed header, dimensions outside the limits,
// maxval != 255, truncated payload, or trailing garbage.
FrameBuffer read_ppm_strict(const std::string& path);

}  // namespace tl
