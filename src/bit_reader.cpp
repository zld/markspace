#include "h264/bit_reader.hpp"

#include <cstdio>
#include <utility>

namespace h264 {

ParseError::ParseError(Diagnostic diag)
    : std::runtime_error(diag.message), diag_(std::move(diag)) {}

void fail(const std::string& message, std::size_t nal_offset,
          std::size_t byte_position, bool has_byte_position) {
  Diagnostic d;
  d.message = message;
  d.nal_offset = nal_offset;
  d.byte_position = byte_position;
  d.has_byte_position = has_byte_position;
  throw ParseError(std::move(d));
}

std::vector<std::uint8_t> rbsp_unescape(const std::uint8_t* data,
                                        std::size_t size,
                                        std::size_t nal_offset) {
  std::vector<std::uint8_t> out;
  out.reserve(size);
  std::size_t zeros = 0;
  for (std::size_t i = 0; i < size; ++i) {
    const std::uint8_t byte = data[i];
    if (zeros >= 2 && byte == 0x03) {
      // emulation_prevention_three_byte: must be followed by 00, 01, 02 or 03.
      if (i + 1 < size && data[i + 1] > 0x03) {
        fail("invalid emulation prevention byte (00 00 03 followed by 0x" +
                 std::string(1, "0123456789abcdef"[(data[i + 1] >> 4) & 0xF]) +
                 std::string(1, "0123456789abcdef"[data[i + 1] & 0xF]) + ")",
             nal_offset, i, true);
      }
      zeros = 0;
      continue;  // drop the escape byte; the next byte starts a fresh run
    }
    out.push_back(byte);
    zeros = (byte == 0) ? zeros + 1 : 0;
  }
  return out;
}

BitReader::BitReader(const std::uint8_t* data, std::size_t size,
                     std::size_t nal_offset)
    : data_(data), size_(size), nal_offset_(nal_offset),
      total_bits_(size * 8) {}

void BitReader::require(std::size_t bits, const char* what) const {
  if (bit_pos_ + bits > total_bits_) {
    fail(std::string("truncated RBSP while reading ") + what, nal_offset_,
         bit_pos_ / 8, true);
  }
}

bool BitReader::read_bit() { return read_bits(1) != 0; }

std::uint32_t BitReader::read_bits(int count) {
  if (count < 1 || count > 32) {
    fail("internal error: invalid bit count in read_bits", nal_offset_,
         bit_pos_ / 8, true);
  }
  require(static_cast<std::size_t>(count), "fixed-width bits");
  std::uint64_t value = 0;
  for (int i = 0; i < count; ++i) {
    const std::size_t byte_index = bit_pos_ >> 3;
    const int bit_index = 7 - static_cast<int>(bit_pos_ & 7u);
    value = (value << 1) | ((data_[byte_index] >> bit_index) & 1u);
    ++bit_pos_;
  }
  return static_cast<std::uint32_t>(value);
}

void BitReader::skip_bits(int count) {
  if (count < 0) {
    fail("internal error: negative skip", nal_offset_, bit_pos_ / 8, true);
  }
  require(static_cast<std::size_t>(count), "skipped bits");
  bit_pos_ += static_cast<std::size_t>(count);
}

std::uint32_t BitReader::read_ue() {
  int leading_zeros = 0;
  while (true) {
    // Bounds first: a code cannot have more than 31 leading zeros.
    if (bit_pos_ >= total_bits_) {
      fail("truncated Exp-Golomb code", nal_offset_, bit_pos_ / 8, true);
    }
    if (read_bit()) break;
    if (++leading_zeros > 31) {
      fail("Exp-Golomb prefix too long", nal_offset_, bit_pos_ / 8, true);
    }
  }
  std::uint64_t value = 0;
  if (leading_zeros > 0) {
    require(static_cast<std::size_t>(leading_zeros), "Exp-Golomb suffix");
    std::uint32_t suffix = read_bits(leading_zeros);
    value = (std::uint64_t{1} << leading_zeros) - 1 + suffix;
  }
  if (value > 0xFFFFFFFEull) {
    fail("Exp-Golomb value overflow", nal_offset_, bit_pos_ / 8, true);
  }
  return static_cast<std::uint32_t>(value);
}

std::int32_t BitReader::read_se() {
  const std::uint32_t code_num = read_ue();
  const std::int64_t magnitude =
      (code_num & 1u) ? (static_cast<std::int64_t>(code_num) + 1) / 2
                      : -static_cast<std::int64_t>(code_num) / 2;
  if (magnitude < INT32_MIN || magnitude > INT32_MAX) {
    fail("signed Exp-Golomb mapping overflow", nal_offset_, bit_pos_ / 8,
         true);
  }
  return static_cast<std::int32_t>(magnitude);
}

bool BitReader::more_rbsp_data() const {
  if (bit_pos_ >= total_bits_) return false;
  // The rbsp_stop_one_bit is the last set bit in the buffer; everything after
  // it is zero padding. Scan back over zero bytes.
  std::size_t stop_bit = total_bits_;
  for (std::size_t i = size_; i > 0; --i) {
    const std::uint8_t byte = data_[i - 1];
    if (byte == 0) continue;
    // Bits are stored MSB-first: a set bit at bit index k occupies stream
    // position 7 - k. The last set bit in stream order is the lowest set bit
    // of the byte.
    int lowest_set = 0;
    while (((byte >> lowest_set) & 1u) == 0) ++lowest_set;
    const int stream_pos = 7 - lowest_set;
    stop_bit = (i - 1) * 8 + static_cast<std::size_t>(stream_pos);
    break;
  }
  return bit_pos_ < stop_bit;
}

}  // namespace h264
