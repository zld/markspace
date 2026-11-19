#pragma once

#include <cstdint>
#include <vector>

namespace h264::gen {

// Minimal big-endian bit writer used only by the deterministic fixture
// generator.  It emits the same bit layout the production BitReader parses,
// including ue(v)/se(v) Exp-Golomb codes, and inserts emulation prevention
// bytes when the byte stream is finalized.
class BitWriter {
public:
    void write_bits(std::uint32_t value, unsigned n);
    void write_ue(std::uint32_t value);
    void write_se(std::int32_t value);
    void write_flag(bool on) { write_bits(on ? 1u : 0u, 1); }
    void align_zero();

    const std::vector<std::uint8_t>& bytes() const { return data_; }
    std::vector<std::uint8_t> take_escaped();

private:
    std::vector<std::uint8_t> data_;
    std::uint64_t accum_ = 0;   // bits buffered MSB-first, not yet flushed
    unsigned pending_ = 0;      // number of valid bits held in accum_
};

// Appends a NAL unit with the given header and RBSP payload, choosing a
// four-byte start code when `four_byte` is set and a three-byte one otherwise.
void append_nal(std::vector<std::uint8_t>& out, std::uint8_t nal_header,
                const std::vector<std::uint8_t>& rbsp, bool four_byte);

// Builds the deterministic demo stream documented in the task:
// 1280x720 SPS, PPS, two GOPs, I/P/B slices, one multi-slice picture and one
// interlaced field pair, mixing three- and four-byte start codes.
std::vector<std::uint8_t> build_demo_stream();

}  // namespace h264::gen
