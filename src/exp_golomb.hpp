#ifndef H264_EXP_GOLOMB_HPP
#define H264_EXP_GOLOMB_HPP

#include <cstdint>

#include "bit_reader.hpp"

namespace h264 {

// H.264 limits Exp-Golomb code words to 32 leading zero bits (the longest
// practical syntax element, cpb_cnt_minus1, is bounded at 31 anyway). A run
// longer than this is corruption rather than a valid code word.
constexpr unsigned kMaxLeadingZeroBits = 32;

// unsigned Exp-Golomb (ue(v)):
//   leadingZeroBits = n
//   codeNum = 2^n - 1 + read_bits(n)
// Truncated code words and over-long zero runs raise ParseError carrying the
// NAL file offset supplied to the BitReader.
inline std::uint32_t read_ue(BitReader& br) {
    unsigned zeros = 0;
    while (zeros <= kMaxLeadingZeroBits) {
        if (br.eof()) {
            throw ParseError(br.byte_offset(),
                             "truncated Exp-Golomb code word (leading zeros)");
        }
        if (br.read_bit() != 0u) {
            break;
        }
        ++zeros;
    }
    if (zeros > kMaxLeadingZeroBits) {
        throw ParseError(br.byte_offset(),
                         "Exp-Golomb leading zero count exceeds 32 bits");
    }
    // Suffix of `zeros` bits; total code word length is 2*zeros + 1.
    const std::uint64_t suffix = zeros == 0u ? 0u : br.read_bits(zeros);
    const std::uint64_t code_num =
        (std::uint64_t{1} << zeros) - 1u + suffix;
    if (code_num > 0xFFFFFFFFULL) {
        throw ParseError(br.byte_offset(),
                         "Exp-Golomb code number exceeds 32-bit range");
    }
    return static_cast<std::uint32_t>(code_num);
}

// signed Exp-Golomb (se(v)), H.264 Table 9-3:
//   codeNum 0 -> 0; odd  -> +(codeNum+1)/2; even -> -codeNum/2
inline std::int32_t read_se(BitReader& br) {
    const std::uint32_t code_num = read_ue(br);
    if (code_num == 0u) {
        return 0;
    }
    if ((code_num & 1u) != 0u) {
        return static_cast<std::int32_t>((code_num + 1u) / 2u);
    }
    return -static_cast<std::int32_t>(code_num / 2u);
}

}  // namespace h264

#endif
