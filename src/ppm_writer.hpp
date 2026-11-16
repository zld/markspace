#ifndef TRANSITION_LAB_PPM_WRITER_HPP
#define TRANSITION_LAB_PPM_WRITER_HPP

#include <string>

#include "frame_buffer.hpp"

namespace tl {

enum class WriteStatus {
    Ok,
    InvalidPath,
    OpenFailed,
    WriteFailed,
    FlushFailed
};

// Reject empty, over-long, NUL-containing paths and parent traversal
// components. The output directory itself must already exist.
bool validate_output_path(const std::string& path);

// Write one frame as binary P6 PPM. The file is first written to a
// temporary sibling path and only renamed into place after a successful
// flush; on any failure the temporary file is removed and an error
// status is returned.
WriteStatus write_ppm(const std::string& path, const FrameBuffer& frame,
                      std::string* error);

const char* write_status_message(WriteStatus status);

}  // namespace tl

#endif
