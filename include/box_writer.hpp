#pragma once

#include "box_reader.hpp"

#include <cstring>
#include <vector>

namespace mp4flow {

// Append-only big-endian MP4 writer with automatic box size patching.
// Every size/offset path is checked against uint64 overflow and the global
// kMaxFileSize cap.
class BoxWriter {
public:
  explicit BoxWriter(Bytes& out) : out_(out) {}

  void u8(uint8_t v);
  void u16(uint16_t v);
  void u32(uint32_t v);
  void u64(uint64_t v);
  void i32(int32_t v) { u32(uint32_t(v)); }
  void i64(int64_t v) { u64(uint64_t(v)); }
  void bytes(const uint8_t* p, size_t n);
  void bytes(const Bytes& b) { bytes(b.data(), b.size()); }
  void zero(size_t n);

  // Open a box of the given fourcc; close it with endBox(). Boxes nest.
  size_t beginBox(uint32_t type);
  // Full box (version + 24-bit flags), payload follows.
  size_t beginFull(uint32_t type, uint8_t version, uint32_t flags);
  void endBox();

  size_t size() const { return out_.size(); }

private:
  void reserve(size_t extra);
  Bytes& out_;
  std::vector<size_t> stack_; // positions of open size fields
};

} // namespace mp4flow
