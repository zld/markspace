// Unsigned and signed Exp-Golomb decoding with shared call statistics.
#pragma once

#include <cstdint>

#include "bit_reader.hpp"

namespace h264inspect {

struct ExpGolombStats {
    std::uint64_t ue_calls = 0;
    std::uint64_t se_calls = 0;
    std::uint64_t ue_failures = 0;
    std::uint64_t se_failures = 0;
    std::uint32_t max_ue_bits = 0;   // longest ue(v) codeword in bits
    std::uint32_t max_se_bits = 0;   // longest se(v) codeword in bits
    std::uint32_t max_leading_zeros = 0;
    std::uint64_t max_ue_value = 0;
    std::uint64_t max_se_magnitude = 0;

    void merge(const ExpGolombStats& other);
};

struct ExpGolombLimits {
    // Leading zeros permitted before the stop bit. 32 keeps codeword value
    // within 64 bits (2 * 32 + 1) and rejects pathological padding runs.
    std::uint32_t max_leading_zeros = 32;
    // Maximum total codeword width in bits.
    std::uint32_t max_codeword_bits = 65;
};

// Decodes from an existing BitReader and records every attempt in `stats`.
// Failure modes: end of RBSP (ErrorKind::Truncated) and overlong codeword
// (ErrorKind::ExpGolomb). On failure the output value is left untouched.
class ExpGolombDecoder {
public:
    ExpGolombDecoder(BitReader& reader, ExpGolombStats& stats,
                     const ExpGolombLimits& limits = ExpGolombLimits())
        : reader_(reader), stats_(stats), limits_(limits) {}

    bool read_ue(std::uint64_t& value, Diagnostic* diagnostic);
    bool read_se(std::int64_t& value, Diagnostic* diagnostic);

    // Convenience wrappers that require the result to fit the target width.
    bool read_ue_u32(std::uint32_t& value, Diagnostic* diagnostic);

    const ExpGolombStats& stats() const { return stats_; }

private:
    bool fail(ErrorKind kind, Diagnostic* diagnostic, const char* message);
    bool read_code(std::uint64_t& code_num, std::uint32_t& bits_used,
                   std::uint32_t& leading_zeros, Diagnostic* diagnostic);

    BitReader& reader_;
    ExpGolombStats& stats_;
    ExpGolombLimits limits_;
};

}  // namespace h264inspect
