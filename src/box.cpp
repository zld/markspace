#include "fmp4/box.hpp"

#include <cstdio>

namespace fmp4 {

BoxError::BoxError(std::string file, uint64_t offset, const std::string& msg)
    : Fmp4Error(kExitFormat,
                file + ":" + std::to_string(offset) + ": " + msg),
      file(std::move(file)),
      offset(offset) {}

std::vector<uint8_t> read_file(const std::string& path) {
  FILE* f = std::fopen(path.c_str(), "rb");
  if (!f) throw Fmp4Error(kExitUsage, "cannot open file: " + path);
  std::fseek(f, 0, SEEK_END);
  long n = std::ftell(f);
  if (n < 0) {
    std::fclose(f);
    throw Fmp4Error(kExitUsage, "cannot seek file: " + path);
  }
  std::fseek(f, 0, SEEK_SET);
  std::vector<uint8_t> data(static_cast<size_t>(n));
  if (n > 0 && std::fread(data.data(), 1, static_cast<size_t>(n), f) !=
                   static_cast<size_t>(n)) {
    std::fclose(f);
    throw Fmp4Error(kExitUsage, "cannot read file: " + path);
  }
  std::fclose(f);
  return data;
}

void write_file(const std::string& path, const std::vector<uint8_t>& data) {
  FILE* f = std::fopen(path.c_str(), "wb");
  if (!f) throw Fmp4Error(kExitUsage, "cannot write file: " + path);
  if (!data.empty() &&
      std::fwrite(data.data(), 1, data.size(), f) != data.size()) {
    std::fclose(f);
    throw Fmp4Error(kExitUsage, "cannot write file: " + path);
  }
  if (std::fclose(f) != 0)
    throw Fmp4Error(kExitUsage, "cannot close file: " + path);
}

static uint32_t be32(const uint8_t* p) {
  return (uint32_t)p[0] << 24 | (uint32_t)p[1] << 16 | (uint32_t)p[2] << 8 |
         (uint32_t)p[3];
}

static uint64_t be64(const uint8_t* p) {
  return (uint64_t)be32(p) << 32 | (uint64_t)be32(p + 4);
}

bool is_container_type(const std::string& t) {
  static const char* kContainers[] = {"moov", "trak", "mdia", "minf", "stbl",
                                      "mvex", "moof", "traf", "dinf", "edts"};
  for (const char* c : kContainers)
    if (t == c) return true;
  return false;
}

std::vector<Box> parse_boxes(const uint8_t* data, size_t len,
                             const std::string& file, uint64_t base_offset,
                             bool allow_size_zero) {
  std::vector<Box> out;
  size_t pos = 0;
  while (pos < len) {
    uint64_t abs_off = base_offset + pos;
    if (len - pos < 8) throw BoxError(file, abs_off, "truncated box header");
    uint32_t size32 = be32(data + pos);
    std::string type(reinterpret_cast<const char*>(data + pos + 4), 4);
    uint64_t size = 0;
    uint64_t header = 8;
    if (size32 == 1) {
      if (len - pos < 16)
        throw BoxError(file, abs_off, "truncated extended box header");
      size = be64(data + pos + 8);
      header = 16;
      if (size < 16)
        throw BoxError(file, abs_off, "extended box size smaller than header");
    } else if (size32 == 0) {
      if (!allow_size_zero)
        throw BoxError(file, abs_off,
                       "size-0 box only allowed at end of file");
      size = len - pos;
    } else {
      size = size32;
      if (size < 8) throw BoxError(file, abs_off, "box size smaller than header");
    }
    // size <= len - pos also guards against offset+size overflow.
    if (size > len - pos)
      throw BoxError(file, abs_off, "box extends past end of data (truncated)");
    out.push_back(Box{type, abs_off, size, header});
    pos += static_cast<size_t>(size);
  }
  return out;
}

std::vector<Box> parse_children(const uint8_t* file_data, const Box& parent,
                                const std::string& file) {
  if (!is_container_type(parent.type))
    throw BoxError(file, parent.offset,
                   "box type " + parent.type + " cannot contain children");
  return parse_boxes(file_data + parent.payload_offset(),
                     static_cast<size_t>(parent.payload_size()), file,
                     parent.payload_offset(), false);
}

const Box* find_box(const std::vector<Box>& boxes, const std::string& type) {
  for (const auto& b : boxes)
    if (b.type == type) return &b;
  return nullptr;
}

Reader::Reader(const uint8_t* data, size_t len, std::string file,
               uint64_t base_offset)
    : data_(data), len_(len), file_(std::move(file)), base_(base_offset) {}

void Reader::require(size_t n, const char* what) {
  if (n > len_ - pos_)
    throw BoxError(file_, abs_pos(),
                   std::string("truncated ") + what);
}

uint8_t Reader::u8() {
  require(1, "u8");
  return data_[pos_++];
}

uint16_t Reader::u16() {
  require(2, "u16");
  uint16_t v = (uint16_t)(data_[pos_] << 8 | data_[pos_ + 1]);
  pos_ += 2;
  return v;
}

uint32_t Reader::u24() {
  require(3, "u24");
  uint32_t v = (uint32_t)data_[pos_] << 16 | (uint32_t)data_[pos_ + 1] << 8 |
               (uint32_t)data_[pos_ + 2];
  pos_ += 3;
  return v;
}

uint32_t Reader::u32() {
  require(4, "u32");
  uint32_t v = be32(data_ + pos_);
  pos_ += 4;
  return v;
}

uint64_t Reader::u64() {
  require(8, "u64");
  uint64_t v = be64(data_ + pos_);
  pos_ += 8;
  return v;
}

int32_t Reader::i32() { return static_cast<int32_t>(u32()); }

std::string Reader::fourcc() {
  require(4, "fourcc");
  std::string t(reinterpret_cast<const char*>(data_ + pos_), 4);
  pos_ += 4;
  return t;
}

void BoxWriter::u8(uint8_t v) { buf.push_back(v); }
void BoxWriter::u16(uint16_t v) {
  buf.push_back((uint8_t)(v >> 8));
  buf.push_back((uint8_t)v);
}
void BoxWriter::u24(uint32_t v) {
  buf.push_back((uint8_t)(v >> 16));
  buf.push_back((uint8_t)(v >> 8));
  buf.push_back((uint8_t)v);
}
void BoxWriter::u32(uint32_t v) {
  buf.push_back((uint8_t)(v >> 24));
  buf.push_back((uint8_t)(v >> 16));
  buf.push_back((uint8_t)(v >> 8));
  buf.push_back((uint8_t)v);
}
void BoxWriter::u64(uint64_t v) {
  u32((uint32_t)(v >> 32));
  u32((uint32_t)v);
}
void BoxWriter::i32(int32_t v) { u32(static_cast<uint32_t>(v)); }
void BoxWriter::fourcc(const char* t) { bytes(t, 4); }
void BoxWriter::bytes(const void* p, size_t n) {
  const uint8_t* b = static_cast<const uint8_t*>(p);
  buf.insert(buf.end(), b, b + n);
}
void BoxWriter::zeros(size_t n) { buf.insert(buf.end(), n, 0); }

size_t BoxWriter::begin_box(const char* type) {
  size_t pos = buf.size();
  u32(0);
  fourcc(type);
  return pos;
}

void BoxWriter::end_box(size_t size_pos) {
  patch_u32(size_pos, (uint32_t)(buf.size() - size_pos));
}

size_t BoxWriter::begin_full_box(const char* type, uint8_t version,
                                 uint32_t flags) {
  size_t pos = begin_box(type);
  u8(version);
  u24(flags);
  return pos;
}

void BoxWriter::patch_u32(size_t pos, uint32_t v) {
  buf[pos] = (uint8_t)(v >> 24);
  buf[pos + 1] = (uint8_t)(v >> 16);
  buf[pos + 2] = (uint8_t)(v >> 8);
  buf[pos + 3] = (uint8_t)v;
}

void BoxWriter::patch_i32(size_t pos, int32_t v) {
  patch_u32(pos, static_cast<uint32_t>(v));
}

}  // namespace fmp4
