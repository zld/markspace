#ifndef H264_FIXTURE_WRITER_HPP
#define H264_FIXTURE_WRITER_HPP

#include <cstdint>
#include <vector>

namespace h264 {

// MSB-first bit writer with Exp-Golomb encoding and RBSP stop-bit padding.
class BitWriter {
public:
    void writeBits(std::uint32_t value, int n);
    void writeUe(std::uint32_t codeNum);
    void writeSe(std::int32_t value);
    // Appends rbsp_stop_one_bit plus zero alignment bits.
    void rbspStop();
    const std::vector<std::uint8_t>& bytes() const { return buf_; }

private:
    std::vector<std::uint8_t> buf_;
    std::size_t bitPos_ = 0;
};

// Insert H.264 emulation prevention bytes (00 00 03).
std::vector<std::uint8_t> rbspEscape(const std::vector<std::uint8_t>& rbsp);

// Builds synthetic, spec-shaped H.264 streams entirely in code. No external
// samples are referenced.
class FixtureWriter {
public:
    // Full demo stream: AUD, SPS (640x360 cropped, VUI timing), PPS, a
    // 3-slice IDR, P/B/P frames, a field-coded (interlaced) frame and a
    // second IDR that opens another group. Annex B framing.
    std::vector<std::uint8_t> buildSample();

    // Same NAL payloads with 4-byte big-endian length prefixes.
    std::vector<std::uint8_t> buildSampleAvcc();

    // Corrupted variants for recovery tests.
    std::vector<std::uint8_t> buildTruncatedNal();   // cut mid NAL
    std::vector<std::uint8_t> buildBadExpGolomb();   // 32 leading zeros
    std::vector<std::uint8_t> buildUnknownNalType(); // nal_type 30
    std::vector<std::uint8_t> buildMidStreamSpsUpdate();

    void reset() { out_.clear(); }
    const std::vector<std::uint8_t>& buffer() const { return out_; }

private:
    void appendAnnexB(std::uint8_t nalHeader,
                      const std::vector<std::uint8_t>& rbsp);
    void appendAvcc(std::uint8_t nalHeader,
                    const std::vector<std::uint8_t>& rbsp);
    std::vector<std::uint8_t> sampleNals() ;

    std::vector<std::uint8_t> out_;
};

} // namespace h264

#endif
