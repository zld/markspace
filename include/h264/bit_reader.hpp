#pragma once

#include <cstddef>
#include <cstdint>
#include <stdexcept>
#include <string>
#include <vector>

namespace h264 {

// A structured parse failure. Every failure carries the zero-based file offset
// of the start code of the NAL unit being parsed so callers can locate it.
struct Diagnostic {
  std::string message;
  std::size_t nal_offset = 0;
  std::size_t byte_position = 0;
  bool has_byte_position = false;
};

class ParseError : public std::runtime_error {
 public:
  explicit ParseError(Diagnostic diag);
  const Diagnostic& diagnostic() const noexcept { return diag_; }

 private:
  Diagnostic diag_;
};

[[noreturn]] void fail(const std::string& message, std::size_t nal_offset,
                       std::size_t byte_position = 0,
                       bool has_byte_position = false);

// Removes H.264 emulation-prevention bytes (00 00 03 -> 00 00) from a NAL unit
// payload. Throws on a malformed escape sequence.
std::vector<std::uint8_t> rbsp_unescape(const std::uint8_t* data,
                                        std::size_t size,
                                        std::size_t nal_offset);

// Bounded bit reader over an RBSP buffer. No read may cross the end of the
// buffer; running out of data raises ParseError.
class BitReader {
 public:
  BitReader(const std::uint8_t* data, std::size_t size,
            std::size_t nal_offset);

  bool read_bit();
  std::uint32_t read_bits(int count);  // 1..32 bits
  void skip_bits(int count);
  std::uint32_t read_ue();  // Exp-Golomb unsigned
  std::int32_t read_se();   // Exp-Golomb signed

  std::size_t bits_remaining() const noexcept {
    return total_bits_ - bit_pos_;
  }
  std::size_t bit_position() const noexcept { return bit_pos_; }
  bool byte_aligned() const noexcept { return (bit_pos_ & 7u) == 0; }
  bool more_rbsp_data() const;

 private:
  void require(std::size_t bits, const char* what) const;

  const std::uint8_t* data_;
  std::size_t size_;
  std::size_t nal_offset_;
  std::size_t bit_pos_ = 0;
  std::size_t total_bits_ = 0;
};

}  // namespace h264
