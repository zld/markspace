#pragma once

#include <cstdint>
#include <vector>

namespace h264 {

// Exp-Golomb / fixed-width bit writer used to build deterministic fixtures and
// self-test inputs.
class BitWriter {
 public:
  void write_bit(bool bit);
  void write_bits(std::uint32_t value, int count);
  void write_ue(std::uint32_t value);
  void write_se(std::int32_t value);
  void align_zero();
  void append_trailing_bits();  // rbsp_trailing_bits(): 1 then zero padding

  const std::vector<std::uint8_t>& bytes() const noexcept { return bytes_; }
  std::size_t bit_count() const noexcept { return bit_count_; }

 private:
  std::vector<std::uint8_t> bytes_;
  std::size_t bit_count_ = 0;
};

// Inserts emulation-prevention bytes into an RBSP payload.
std::vector<std::uint8_t> rbsp_escape(const std::vector<std::uint8_t>& rbsp);

}  // namespace h264
