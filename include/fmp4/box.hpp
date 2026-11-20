#pragma once

#include <cstddef>
#include <cstdint>
#include <stdexcept>
#include <string>
#include <vector>

namespace fmp4 {

// Exit codes used across the tool.
inline constexpr int kExitOk = 0;
inline constexpr int kExitUsage = 2;    // bad arguments or file I/O errors
inline constexpr int kExitFormat = 3;   // corrupt boxes / unsupported versions or flags
inline constexpr int kExitSequence = 4; // missing or duplicate moof sequence numbers
inline constexpr int kExitInit = 5;     // init segment incompatible
inline constexpr int kExitDelta = 6;    // decode-time delta beyond tolerance

class Fmp4Error : public std::runtime_error {
 public:
  Fmp4Error(int code, const std::string& msg) : std::runtime_error(msg), code(code) {}
  int code;
};

// Format error carrying the file and byte offset of the offending box.
class BoxError : public Fmp4Error {
 public:
  BoxError(std::string file, uint64_t offset, const std::string& msg);
  std::string file;
  uint64_t offset;
};

struct Box {
  std::string type;      // 4-character box type
  uint64_t offset = 0;   // absolute offset of the box header in the file
  uint64_t size = 0;     // total size including header
  uint64_t header_size = 0;
  uint64_t payload_offset() const { return offset + header_size; }
  uint64_t payload_size() const { return size - header_size; }
};

std::vector<uint8_t> read_file(const std::string& path);
void write_file(const std::string& path, const std::vector<uint8_t>& data);

// Parses a sequence of sibling boxes from [data, data+len). base_offset is the
// absolute file offset of data[0], used only for error reporting. A size-0 box
// (extends to end of data) is only allowed when allow_size_zero is true, which
// callers should restrict to top-level parsing so it can only appear at the
// very end of a file. Truncation, overrun and invalid sizes throw BoxError.
std::vector<Box> parse_boxes(const uint8_t* data, size_t len, const std::string& file,
                             uint64_t base_offset = 0, bool allow_size_zero = true);

// Parses the children of a container box. file_data must be the whole-file
// buffer the parent Box was parsed from.
std::vector<Box> parse_children(const uint8_t* file_data, const Box& parent,
                                const std::string& file);

const Box* find_box(const std::vector<Box>& boxes, const std::string& type);
bool is_container_type(const std::string& type);

// Big-endian reader over a box payload; every read is bounds-checked and
// reports absolute file offsets on truncation.
class Reader {
 public:
  Reader(const uint8_t* data, size_t len, std::string file, uint64_t base_offset);
  uint8_t u8();
  uint16_t u16();
  uint32_t u24();
  uint32_t u32();
  uint64_t u64();
  int32_t i32();
  std::string fourcc();
  size_t remaining() const { return len_ - pos_; }
  uint64_t abs_pos() const { return base_ + pos_; }
  void require(size_t n, const char* what);
  void skip(size_t n) {
    require(n, "skip");
    pos_ += n;
  }

 private:
  const uint8_t* data_;
  size_t len_;
  size_t pos_ = 0;
  std::string file_;
  uint64_t base_;
};

// Big-endian box serializer with size patching.
class BoxWriter {
 public:
  std::vector<uint8_t> buf;
  void u8(uint8_t v);
  void u16(uint16_t v);
  void u24(uint32_t v);
  void u32(uint32_t v);
  void u64(uint64_t v);
  void i32(int32_t v);
  void fourcc(const char* t);
  void bytes(const void* p, size_t n);
  void zeros(size_t n);
  // Writes a 32-bit size placeholder plus type; returns the size field
  // position for end_box().
  size_t begin_box(const char* type);
  void end_box(size_t size_pos);
  // begin_box + full-box version/flags header.
  size_t begin_full_box(const char* type, uint8_t version, uint32_t flags);
  void end_full_box(size_t size_pos) { end_box(size_pos); }
  void patch_u32(size_t pos, uint32_t v);
  void patch_i32(size_t pos, int32_t v);
};

}  // namespace fmp4
