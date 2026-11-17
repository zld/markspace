#pragma once
// ISO BMFF box reader: bounded, streaming-friendly parsing with parent-boundary,
// integer-overflow and truncation checks. Unknown boxes are skipped via offsets.
#include <cstdint>
#include <cstdio>
#include <limits>
#include <stdexcept>
#include <string>
#include <vector>

namespace fmp4 {

inline constexpr uint32_t FOURCC(const char (&s)[5]) {
  return (uint32_t(uint8_t(s[0])) << 24) | (uint32_t(uint8_t(s[1])) << 16) |
         (uint32_t(uint8_t(s[2])) << 8) | uint32_t(uint8_t(s[3]));
}

// Error carrying the source file name and byte offset where parsing failed.
class Fmp4Error : public std::runtime_error {
public:
  Fmp4Error(const std::string &msg, std::string file = "", uint64_t off = 0)
      : std::runtime_error(make(msg, file, off)), file_(std::move(file)),
        offset_(off) {}
  const std::string &file() const noexcept { return file_; }
  uint64_t offset() const noexcept { return offset_; }

private:
  static std::string make(const std::string &msg, const std::string &f,
                          uint64_t off);
  std::string file_;
  uint64_t offset_;
};

struct ParseLimits {
  uint64_t max_box_size = 1ull << 33; // reject implausibly large boxes
  uint64_t max_total_meta = 64ull * 1024 * 1024; // metadata memory cap
  uint32_t max_depth = 32;
  uint32_t max_box_count = 200000;
};

// Abstract bounded byte source; reads past the declared end throw Fmp4Error.
class IDataSource {
public:
  virtual ~IDataSource() = default;
  virtual void read(uint64_t off, void *dst, size_t n) = 0;
  virtual uint64_t size() const = 0;
  virtual const std::string &name() const = 0;
};

class FileSource final : public IDataSource {
public:
  explicit FileSource(const std::string &path);
  ~FileSource() override;
  FileSource(const FileSource &) = delete;
  FileSource &operator=(const FileSource &) = delete;
  void read(uint64_t off, void *dst, size_t n) override;
  uint64_t size() const override { return size_; }
  const std::string &name() const override { return path_; }

private:
  std::FILE *fp_ = nullptr;
  std::string path_;
  uint64_t size_ = 0;
};

class MemorySource final : public IDataSource {
public:
  MemorySource(std::vector<uint8_t> data, std::string name);
  void read(uint64_t off, void *dst, size_t n) override;
  uint64_t size() const override { return data_.size(); }
  const std::string &name() const override { return name_; }

private:
  std::vector<uint8_t> data_;
  std::string name_;
};

struct BoxHeader {
  uint32_t type = 0;
  uint64_t offset = 0; // offset of size field
  uint64_t header_size = 0;
  uint64_t payload_offset = 0;
  uint64_t payload_size = 0;
  uint64_t end() const { return offset + header_size + payload_size; }
};

class BoxReader {
public:
  BoxReader(IDataSource &src, uint64_t start, uint64_t end,
            ParseLimits lim = {});

  // Reads the next box header within [start,end). Returns false at end.
  bool next(BoxHeader &out);
  // Descend into a container box; child cursor bounded by the parent payload.
  BoxReader enter(const BoxHeader &parent, uint32_t depth) const;
  // Bounded payload reads, all big-endian.
  std::vector<uint8_t> read_payload(const BoxHeader &b, size_t cap = 0) const;
  uint8_t read_u8(const BoxHeader &b, uint64_t rel) const;
  uint16_t read_u16(const BoxHeader &b, uint64_t rel) const;
  uint32_t read_u32(const BoxHeader &b, uint64_t rel) const;
  uint64_t read_u64(const BoxHeader &b, uint64_t rel) const;
  // Read n bytes from arbitrary absolute offsets within the window.
  void read_abs(uint64_t off, void *dst, size_t n) const;
  uint64_t start() const { return start_; }
  uint64_t end() const { return end_; }
  uint64_t cursor() const { return cursor_; }
  const ParseLimits &limits() const { return limits_; }
  IDataSource &source() const { return src_; }
  uint32_t boxes_seen() const { return box_count_; }

private:
  void require(uint64_t off, size_t n) const;
  IDataSource &src_;
  uint64_t start_, end_, cursor_;
  ParseLimits limits_;
  uint32_t box_count_ = 0;
  uint32_t depth_ = 0;
};

} // namespace fmp4
