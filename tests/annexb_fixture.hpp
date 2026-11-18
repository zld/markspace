// Annex B fixture synthesis shared by parser tests and --generate-sample.
#pragma once

#include <cstdint>
#include <initializer_list>
#include <string>
#include <vector>

#include "bit_writer.hpp"

namespace selftest {

using BitWriter = ::h264inspect::BitWriter;

inline std::vector<std::uint8_t> annexb_nal(int header, const std::vector<std::uint8_t>& payload,
                                            bool four_byte = true) {
    std::vector<std::uint8_t> out;
    if (four_byte) {
        out = {0x00, 0x00, 0x00, 0x01};
    } else {
        out = {0x00, 0x00, 0x01};
    }
    out.push_back(static_cast<std::uint8_t>(header));
    out.insert(out.end(), payload.begin(), payload.end());
    return out;
}

inline void append(std::vector<std::uint8_t>& dst, const std::vector<std::uint8_t>& src) {
    dst.insert(dst.end(), src.begin(), src.end());
}

// Inserts emulation-prevention bytes so the result is a legal EBSP.
inline std::vector<std::uint8_t> escape_rbsp(const std::vector<std::uint8_t>& rbsp) {
    std::vector<std::uint8_t> out;
    out.reserve(rbsp.size() + 8);
    int zeros = 0;
    for (std::uint8_t byte : rbsp) {
        if (zeros >= 2 && byte <= 0x03) {
            out.push_back(0x03);
            zeros = 0;
        }
        out.push_back(byte);
        zeros = (byte == 0x00) ? zeros + 1 : 0;
    }
    return out;
}

inline std::vector<std::uint8_t> concat(
    std::initializer_list<std::vector<std::uint8_t>> parts) {
    std::vector<std::uint8_t> out;
    for (const auto& part : parts) append(out, part);
    return out;
}

}  // namespace selftest
