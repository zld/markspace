#include "fixture_writer.hpp"

#include <cstring>

namespace h264 {

void BitWriter::writeBits(std::uint32_t value, int n) {
    for (int i = n - 1; i >= 0; --i) {
        if (bitPos_ % 8 == 0) buf_.push_back(0);
        if ((value >> i) & 1u)
            buf_.back() |= static_cast<std::uint8_t>(
                1u << (7 - (bitPos_ % 8)));
        ++bitPos_;
    }
}

void BitWriter::writeUe(std::uint32_t codeNum) {
    std::uint32_t mapped = codeNum + 1;
    int n = 0;
    for (std::uint32_t v = mapped; v > 0; v >>= 1) ++n; // bit length
    for (int i = 0; i < n - 1; ++i) writeBits(0, 1);
    writeBits(mapped, n);
}

void BitWriter::writeSe(std::int32_t value) {
    std::uint32_t codeNum = value > 0
        ? static_cast<std::uint32_t>(2 * value - 1)
        : static_cast<std::uint32_t>(-2 * value);
    writeUe(codeNum);
}

void BitWriter::rbspStop() {
    writeBits(1, 1);
    while (bitPos_ % 8 != 0) writeBits(0, 1);
}

std::vector<std::uint8_t>
rbspEscape(const std::vector<std::uint8_t>& rbsp) {
    std::vector<std::uint8_t> out;
    out.reserve(rbsp.size() + rbsp.size() / 16);
    int zeros = 0;
    for (std::uint8_t b : rbsp) {
        if (zeros >= 2 && b <= 0x03) {
            out.push_back(0x03);
            zeros = 0;
        }
        out.push_back(b);
        zeros = (b == 0x00) ? zeros + 1 : 0;
    }
    return out;
}

namespace {

// --- Synthetic RBSP payloads (frame_mbs_only_flag = 0 allows field coding) ---

std::vector<std::uint8_t> makeSps(int levelIdc = 30) {
    BitWriter w;
    w.writeBits(66, 8);   // profile_idc = Baseline
    w.writeBits(0, 8);    // constraint flags
    w.writeBits(levelIdc, 8);
    w.writeUe(0);         // sps id
    w.writeUe(0);         // log2_max_frame_num_minus1 -> 4
    w.writeUe(0);         // pic_order_cnt_type = 0
    w.writeUe(4);         // log2_max_pic_order_cnt_lsb_minus1 -> 8
    w.writeUe(4);         // max_num_ref_frames
    w.writeBits(0, 1);    // gaps_in_frame_num_allowed
    w.writeUe(39);        // 40 MBs wide -> 640
    w.writeUe(11);        // 12 map units; *2 fields -> 384 high
    w.writeBits(0, 1);    // frame_mbs_only_flag = 0
    w.writeBits(0, 1);    // mb_adaptive_frame_field_flag
    w.writeBits(0, 1);    // direct_8x8_inference_flag
    w.writeBits(1, 1);    // frame_cropping_flag
    w.writeUe(0); w.writeUe(0);   // crop left/right
    w.writeUe(3); w.writeUe(3);   // 6*4 = 24 luma rows -> 360
    w.writeBits(1, 1);    // vui_parameters_present_flag
    w.writeBits(0, 1);    // aspect_ratio_info
    w.writeBits(0, 1);    // overscan
    w.writeBits(0, 1);    // video_signal_type
    w.writeBits(0, 1);    // chroma_loc_info
    w.writeBits(1, 1);    // timing_info_present
    w.writeBits(1, 32);   // num_units_in_tick
    w.writeBits(50, 32);  // time_scale -> 25 fps
    w.writeBits(1, 1);    // fixed_frame_rate
    w.writeBits(0, 1);    // nal_hrd
    w.writeBits(0, 1);    // vcl_hrd
    w.writeBits(0, 1);    // pic_struct_present
    w.writeBits(0, 1);    // bitstream_restriction
    w.rbspStop();
    return w.bytes();
}

std::vector<std::uint8_t> makePps() {
    BitWriter w;
    w.writeUe(0);         // pps id
    w.writeUe(0);         // sps id
    w.writeBits(0, 1);    // entropy coding (CAVLC)
    w.writeBits(0, 1);    // bottom_field_pic_order_in_frame
    w.writeUe(0);         // one slice group
    w.writeUe(0);         // ref idx l0
    w.writeUe(0);         // ref idx l1
    w.writeBits(0, 1);    // weighted_pred
    w.writeBits(0, 2);    // weighted_bipred_idc
    w.writeSe(0); w.writeSe(0); w.writeSe(0);
    w.writeBits(1, 1);    // deblocking control present
    w.writeBits(0, 1);    // constrained intra
    w.writeBits(0, 1);    // redundant pic cnt
    w.rbspStop();
    return w.bytes();
}

std::vector<std::uint8_t>
makeSlice(int sliceType, int frameNum, int firstMb, bool idr,
          bool field = false, bool bottom = false, int pocLsb = 0) {
    BitWriter w;
    w.writeUe(firstMb);
    w.writeUe(sliceType); // 0=P 1=B 2=I
    w.writeUe(0);         // pps id
    w.writeBits(frameNum, 4);
    w.writeBits(field ? 1 : 0, 1); // field_pic_flag
    if (field) w.writeBits(bottom ? 1 : 0, 1);
    if (idr) w.writeUe(0);         // idr_pic_id
    w.writeBits(pocLsb & 0xFF, 8); // pic_order_cnt_lsb
    w.rbspStop();
    return w.bytes();
}

} // namespace

void FixtureWriter::appendAnnexB(std::uint8_t nalHeader,
                                 const std::vector<std::uint8_t>& rbsp) {
    const std::uint8_t sc[4] = {0, 0, 0, 1};
    out_.insert(out_.end(), sc, sc + 4);
    out_.push_back(nalHeader);
    auto ebsp = rbspEscape(rbsp);
    out_.insert(out_.end(), ebsp.begin(), ebsp.end());
}

void FixtureWriter::appendAvcc(std::uint8_t nalHeader,
                               const std::vector<std::uint8_t>& rbsp) {
    auto ebsp = rbspEscape(rbsp);
    std::uint32_t len = static_cast<std::uint32_t>(ebsp.size()) + 1;
    out_.push_back(static_cast<std::uint8_t>((len >> 24) & 0xFF));
    out_.push_back(static_cast<std::uint8_t>((len >> 16) & 0xFF));
    out_.push_back(static_cast<std::uint8_t>((len >> 8) & 0xFF));
    out_.push_back(static_cast<std::uint8_t>(len & 0xFF));
    out_.push_back(nalHeader);
    out_.insert(out_.end(), ebsp.begin(), ebsp.end());
}

std::vector<std::uint8_t> FixtureWriter::sampleNals() {
    // Returns Annex B bytes; reused by every variant.
    out_.clear();
    // AUD (primary_pic_type 7)
    appendAnnexB(0x09, std::vector<std::uint8_t>{0xE0});
    appendAnnexB(0x67, makeSps());
    appendAnnexB(0x68, makePps());
    // Three slices of one IDR access unit.
    appendAnnexB(0x65, makeSlice(2, 0, 0, true, false, false, 0));
    appendAnnexB(0x65, makeSlice(2, 0, 13, true, false, false, 0));
    appendAnnexB(0x65, makeSlice(2, 0, 26, true, false, false, 0));
    appendAnnexB(0x41, makeSlice(0, 1, 0, false, false, false, 2)); // P
    appendAnnexB(0x01, makeSlice(1, 2, 0, false, false, false, 3)); // B
    appendAnnexB(0x41, makeSlice(0, 3, 0, false, false, false, 4)); // P
    // Field-coded interlaced P frame: top field then bottom field.
    appendAnnexB(0x41, makeSlice(0, 4, 0, false, true, false, 6));
    appendAnnexB(0x41, makeSlice(0, 4, 0, false, true, true, 7));
    // Second group opens with an AUD followed by an IDR.
    appendAnnexB(0x09, std::vector<std::uint8_t>{0xE0});
    appendAnnexB(0x65, makeSlice(2, 0, 0, true, false, false, 8));
    return out_;
}

std::vector<std::uint8_t> FixtureWriter::buildSample() {
    return sampleNals();
}

std::vector<std::uint8_t> FixtureWriter::buildSampleAvcc() {
    std::vector<std::uint8_t> annexB = sampleNals();
    out_.clear();
    // Re-parse the just-built Annex B units and re-wrap each complete NAL
    // (header byte + RBSP) with a 4-byte big-endian length prefix.
    std::size_t i = 0;
    const std::size_t n = annexB.size();
    while (i + 3 < n && annexB[i] == 0 && annexB[i + 1] == 0) {
        std::size_t codeLen = (annexB[i + 2] == 1) ? 3 : 4;
        std::size_t payload = i + codeLen;
        std::size_t next = n;
        for (std::size_t j = payload; j + 2 < n; ++j) {
            if (annexB[j] == 0 && annexB[j + 1] == 0 &&
                (annexB[j + 2] == 1 ||
                 (j + 3 < n && annexB[j + 2] == 0 &&
                  annexB[j + 3] == 1))) {
                next = j;
                break;
            }
        }
        std::uint8_t header = annexB[payload];
        std::vector<std::uint8_t> rbsp(annexB.begin() + payload + 1,
                                       annexB.begin() + next);
        appendAvcc(header, rbsp);
        i = next;
    }
    return out_;
}

std::vector<std::uint8_t> FixtureWriter::buildTruncatedNal() {
    out_.clear();
    appendAnnexB(0x09, std::vector<std::uint8_t>{0xE0});
    appendAnnexB(0x67, makeSps());
    appendAnnexB(0x68, makePps());
    // Start code + NAL header with no remaining payload: truncated slice.
    const std::uint8_t sc[4] = {0, 0, 0, 1};
    out_.insert(out_.end(), sc, sc + 4);
    out_.push_back(0x65);
    return out_;
}

std::vector<std::uint8_t> FixtureWriter::buildBadExpGolomb() {
    out_.clear();
    appendAnnexB(0x09, std::vector<std::uint8_t>{0xE0});
    appendAnnexB(0x67, makeSps());
    appendAnnexB(0x68, makePps());
    // Slice whose first syntax element is 32 leading zero bits: exceeds
    // the 31 leading-zero Exp-Golomb limit.
    BitWriter w;
    for (int i = 0; i < 32; ++i) w.writeBits(0, 1);
    w.rbspStop();
    appendAnnexB(0x41, w.bytes());
    return out_;
}

std::vector<std::uint8_t> FixtureWriter::buildUnknownNalType() {
    std::vector<std::uint8_t> base = sampleNals();
    // nal_unit_type 30 is unspecified/reserved; append it before a fresh IDR
    // so tolerant scanning can demonstrate recovery past the bad unit.
    BitWriter w;
    w.writeBits(0xAA, 8);
    w.rbspStop();
    appendAnnexB(0x1E, w.bytes());
    appendAnnexB(0x65, makeSlice(2, 0, 0, true, false, false, 12));
    std::vector<std::uint8_t> result = out_;
    out_ = std::move(base);
    return result;
}

std::vector<std::uint8_t> FixtureWriter::buildMidStreamSpsUpdate() {
    sampleNals();
    // Insert an updated SPS (different level) immediately before the final
    // IDR by rebuilding: simplest is appending a new SPS + IDR tail.
    appendAnnexB(0x67, makeSps(31));
    appendAnnexB(0x65, makeSlice(2, 0, 0, true, false, false, 10));
    return out_;
}

} // namespace h264
