#include "h264/bit_writer.hpp"

#include <cstdio>
#include <stdexcept>

namespace h264 {

void BitWriter::write_bit(bool bit) {
  if ((bit_count_ & 7u) == 0) bytes_.push_back(0);
  if (bit) {
    bytes_.back() |= static_cast<std::uint8_t>(0x80u >> (bit_count_ & 7u));
  }
  ++bit_count_;
}

void BitWriter::write_bits(std::uint32_t value, int count) {
  if (count < 0 || count > 32) {
    throw std::invalid_argument("BitWriter::write_bits count out of range");
  }
  for (int i = count - 1; i >= 0; --i) write_bit(((value >> i) & 1u) != 0);
}

void BitWriter::write_ue(std::uint32_t value) {
  if (value > 0xFFFFFFFEu) {
    throw std::invalid_argument("ue(v) value out of range");
  }
  const std::uint64_t code_num = static_cast<std::uint64_t>(value) + 1;
  int leading_zeros = 0;
  while ((code_num >> (leading_zeros + 1)) != 0) ++leading_zeros;
  for (int i = 0; i < leading_zeros; ++i) write_bit(false);
  write_bits(static_cast<std::uint32_t>(code_num), leading_zeros + 1);
}

void BitWriter::write_se(std::int32_t value) {
  if (value == 0) {
    write_ue(0);
  } else if (value > 0) {
    write_ue(
        static_cast<std::uint32_t>(2 * static_cast<std::int64_t>(value) - 1));
  } else {
    write_ue(
        static_cast<std::uint32_t>(-2 * static_cast<std::int64_t>(value)));
  }
}

void BitWriter::align_zero() {
  while ((bit_count_ & 7u) != 0) write_bit(false);
}

void BitWriter::append_trailing_bits() {
  write_bit(true);
  align_zero();
}

std::vector<std::uint8_t> rbsp_escape(const std::vector<std::uint8_t>& rbsp) {
  std::vector<std::uint8_t> out;
  out.reserve(rbsp.size() + rbsp.size() / 8 + 4);
  std::size_t zeros = 0;
  for (std::size_t i = 0; i < rbsp.size(); ++i) {
    const std::uint8_t byte = rbsp[i];
    if (zeros >= 2 && byte <= 0x03) {
      out.push_back(0x03);
      zeros = 0;
    }
    out.push_back(byte);
    zeros = (byte == 0) ? zeros + 1 : 0;
  }
  return out;
}

}  // namespace h264
