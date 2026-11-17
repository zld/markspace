#pragma once

#include <string>

#include "frame_buffer.hpp"

namespace tl {

// Writes a binary P6 PPM atomically: bytes go to "<path>.tmp" and are
// renamed into place. On any failure the temp file is removed, false is
// returned and err describes the problem.
bool write_ppm(const FrameBuffer& fb, const std::string& path,
               std::string& err);

// Re-reads a P6 PPM and checks magic, dimensions, maxval and every pixel
// against fb. false + err on any mismatch or read failure.
bool verify_ppm(const FrameBuffer& fb, const std::string& path,
                std::string& err);

// Rejects empty paths and any ".." component (path traversal).
bool path_is_safe(const std::string& p);

}  // namespace tl
