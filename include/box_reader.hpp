#pragma once

#include <cstdint>
#include <initializer_list>
#include <stdexcept>
#include <string>
#include <vector>

namespace mp4flow {

// Hard cap for any single input/output file (constraint: limit file sizes).
inline constexpr uint64_t kMaxFileSize = 512ull * 1024 * 1024;

enum class ErrorKind {
  Truncated,      // box or field extends past file/parent bounds
  LengthOverflow, // declared sizes or offset arithmetic overflow
  IllegalNesting, // structurally invalid box nesting
  BadValue,       // illegal field value (versions, flags, offsets ...)
  Io,             // filesystem read/write failure
};

struct Mp4Error : std::runtime_error {
  ErrorKind kind;
  std::string path; // box path, e.g. "moof/traf/tfdt"
  Mp4Error(ErrorKind k, std::string boxPath, const std::string& msg);
};

constexpr uint32_t fourcc(const char (&s)[5]) {
  return (uint32_t(uint8_t(s[0])) << 24) | (uint32_t(uint8_t(s[1])) << 16) |
         (uint32_t(uint8_t(s[2])) << 8) | uint32_t(uint8_t(s[3]));
}
std::string typeName(uint32_t t);

struct Box {
  uint32_t type = 0;
  uint64_t start = 0;       // absolute offset of the box header
  uint64_t headerSize = 0;  // 8 (32-bit size) or 16 (largesize)
  uint64_t size = 0;        // total size including the header
  std::vector<Box> children;

  uint64_t payloadOffset() const { return start + headerSize; }
  uint64_t payloadSize() const { return size - headerSize; }
  const Box* findChild(uint32_t t) const;
};

// Walk a type path such as {moov, trak, mdia, mdhd}; nullptr when absent.
const Box* findPath(const Box& root, std::initializer_list<uint32_t> path);
// First box of the given type anywhere underneath root.
const Box* findDeep(const Box& root, uint32_t t);

using Bytes = std::vector<uint8_t>;

struct ParsedFile {
  std::string path;
  Bytes data;
  std::vector<Box> boxes; // top-level boxes
};

ParsedFile parseFile(const std::string& path);

// Parse every box inside [start, end) of base. Nested containers are parsed
// recursively; unknown boxes are retained as leaves with their raw ranges.
std::vector<Box> parseBoxes(const uint8_t* base, uint64_t start, uint64_t end,
                            bool topLevel);

// Bounded big-endian reader; every access is range checked against the buffer.
class Cursor {
public:
  Cursor(const uint8_t* base, uint64_t cap, uint64_t off, std::string path);

  uint8_t u8();
  uint16_t u16();
  uint32_t u32();
  uint64_t u64();
  int32_t i32();
  int64_t i64();
  void skip(uint64_t n);
  const uint8_t* take(uint64_t n);
  void require(uint64_t n) const;

  uint64_t pos() const { return off_; }
  uint64_t remaining() const { return cap_ - off_; }
  std::string path;

private:
  const uint8_t* base_;
  uint64_t cap_;
  uint64_t off_;
};

struct FullHeader {
  uint8_t version;
  uint32_t flags; // 24-bit
};
FullHeader readFull(Cursor& c);

// Overflow-checked additions for offset/size math.
uint64_t checkedAdd(uint64_t a, uint64_t b, const std::string& path);

} // namespace mp4flow
