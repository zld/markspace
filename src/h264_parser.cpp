#include "h264_parser.hpp"
#include "bit_reader.hpp"
#include "exp_golomb.hpp"

#include <algorithm>
#include <cstring>

namespace h264 {

const char* frameTypeName(FrameType t) {
    switch (t) {
        case FT_P: return "P";
        case FT_B: return "B";
        case FT_I: return "I";
        default: return "unknown";
    }
}

FrameType mapSliceType(int v) {
    switch (v) {
        case 0: return FT_P;
        case 1: return FT_B;
        case 2: return FT_I;
        default: return FT_UNKNOWN;
    }
}

namespace {

void skipScalingList(BitReader& br, int size) {
    int lastScale = 8, nextScale = 8;
    for (int j = 0; j < size; ++j) {
        if (nextScale != 0) {
            if (br.readFlag()) nextScale = lastScale;
            else {
                std::int32_t delta = decodeSe(br);
                if (br.error()) return;
                nextScale = (lastScale + delta + 256) % 256;
            }
        }
        if (nextScale != 0) lastScale = nextScale;
    }
}

void skipScalingMatrices(BitReader& br, bool highProfile) {
    int lists = highProfile ? 8 : 8;
    for (int i = 0; i < lists; ++i) {
        if (!br.readFlag()) continue;
        skipScalingList(br, (i < 6) ? 16 : 64);
        if (br.error()) return;
    }
}

// Parse (and mostly skip) VUI; retain timing info when present. Every
// conditional block is only entered when its present flag is set, otherwise
// the reader would consume bits belonging to following fields. Reads stop at
// the RBSP payload boundary (stop bit excluded).
void parseVui(BitReader& br, SpsData& sps, std::size_t dataBits) {
    sps.vuiPresent = true;
    auto more = [&]() -> bool {
        return !br.error() && br.bitPosition() < dataBits;
    };
    if (more() && br.readFlag()) {  // aspect_ratio_info_present
        std::uint32_t ar = br.readU(8);
        if (ar == 255) br.readU(16), br.readU(16);
    }
    if (more() && br.readFlag()) br.readU(1);                 // overscan
    if (more() && br.readFlag()) {                            // video_signal
        br.readU(3); br.readU(1);
        if (br.readFlag()) br.readU(8);             // colour_description
    }
    if (more() && br.readFlag()) {                            // chroma_loc
        decodeUe(br); decodeUe(br);
    }
    if (more() && br.readFlag()) {                            // timing_info
        sps.timingInfoPresent = true;
        sps.numUnitsInTick = br.readU(32);
        sps.timeScale = br.readU(32);
        sps.fixedFrameRateFlag = br.readFlag();
        if (sps.numUnitsInTick > 0 && sps.timeScale > 0)
            sps.frameRate = static_cast<double>(sps.timeScale) /
                            (2.0 * sps.numUnitsInTick);
    }
    auto skipHrd = [&]() {
        std::uint32_t n = decodeUe(br) + 1;
        br.readU(8); br.readU(8);
        for (std::uint32_t i = 0; i < n && !br.error(); ++i) {
            br.readU(1); br.readU(1); br.readU(1);
            decodeUe(br); decodeUe(br); decodeUe(br); decodeUe(br);
            decodeUe(br); br.readU(1);
        }
    };
    if (more() && br.readFlag()) skipHrd();   // nal_hrd
    if (more() && br.readFlag()) skipHrd();   // vcl_hrd
    if (more() && br.readFlag()) br.readU(1); // low_delay_hrd
    if (more() && br.readFlag()) { br.readU(2); br.readU(1); } // pic_struct
    if (more() && br.readFlag()) {            // bitstream_restriction
        br.readU(1); decodeUe(br); decodeUe(br);
        decodeUe(br); decodeUe(br);
    }
}

} // namespace

void H264Parser::parseSps(const NalUnit& nal, const std::uint8_t* base,
                          bool tolerant) {
    bool truncated = false;
    std::vector<std::uint8_t> rbsp =
        rbspUnescape(base + nal.offset, nal.size, &truncated);
    if (rbsp.size() < 2 || truncated) {
        errors_.push_back({nal.offset, "sps", "truncated RBSP"});
        if (!tolerant) return;
    }
    BitReader br(rbsp.data(), rbsp.size());
    std::size_t dataBits = BitReader::rbspDataBits(rbsp.data(), rbsp.size());
    br.readU(8); // NAL header
    SpsData s;
    s.profileIdc = static_cast<int>(br.readU(8));
    s.constraintFlags = static_cast<int>(br.readU(8));
    s.levelIdc = static_cast<int>(br.readU(8));
    s.spsId = static_cast<int>(decodeUe(br));
    bool high = s.profileIdc == 100 || s.profileIdc == 110 ||
                s.profileIdc == 122 || s.profileIdc == 244 ||
                s.profileIdc == 44  || s.profileIdc == 83  ||
                s.profileIdc == 86  || s.profileIdc == 118 ||
                s.profileIdc == 128;
    if (high) {
        s.chromaFormatIdc = static_cast<int>(decodeUe(br));
        if (s.chromaFormatIdc == 3) br.readFlag();
        decodeUe(br); decodeUe(br); br.readFlag();
        skipScalingMatrices(br, true);
    }
    s.log2MaxFrameNum = static_cast<int>(decodeUe(br)) + 4;
    s.picOrderCntType = static_cast<int>(decodeUe(br));
    if (s.picOrderCntType == 0)
        s.log2MaxPicOrderCntLsb = static_cast<int>(decodeUe(br)) + 4;
    else if (s.picOrderCntType == 1) {
        br.readFlag();
        decodeSe(br); decodeSe(br);
        std::uint32_t n = decodeUe(br);
        for (std::uint32_t i = 0; i < n && !br.error(); ++i) decodeSe(br);
    }
    s.maxNumRefFrames = static_cast<int>(decodeUe(br));
    br.readFlag(); // gaps_in_frame_num_allowed
    s.picWidthInMbs = static_cast<int>(decodeUe(br)) + 1;
    s.picHeightInMapUnits = static_cast<int>(decodeUe(br)) + 1;
    s.frameMbsOnlyFlag = br.readFlag();
    if (!s.frameMbsOnlyFlag) s.mbAdaptiveFrameFieldFlag = br.readFlag();
    br.readFlag(); // direct_8x8_inference
    s.frameCroppingFlag = br.readFlag();
    if (s.frameCroppingFlag) {
        s.cropLeft = static_cast<int>(decodeUe(br));
        s.cropRight = static_cast<int>(decodeUe(br));
        s.cropTop = static_cast<int>(decodeUe(br));
        s.cropBottom = static_cast<int>(decodeUe(br));
    }
    // VUI is optional and runs only within payload bits (the RBSP stop bit
    // and alignment padding are excluded by dataBits).
    if (!br.error() && br.bitPosition() < dataBits && br.readFlag())
        parseVui(br, s, dataBits);
    // Reads that merely reach the RBSP stop boundary are not syntax errors.
    if (br.error() && br.bitPosition() > dataBits) {
        errors_.push_back({nal.offset, "sps", "truncated or invalid syntax"});
        if (!tolerant) return;
    }
    s.widthMbUnits = s.picWidthInMbs;
    s.heightMapUnits = s.picHeightInMapUnits;
    s.frameWidth = s.picWidthInMbs * 16;
    s.frameHeight = s.picHeightInMapUnits * 16 *
                    (s.frameMbsOnlyFlag ? 1 : 2);
    int subX = (s.chromaFormatIdc == 0) ? 1 : 2;
    int subY = (s.chromaFormatIdc == 0) ? 1
             : (s.chromaFormatIdc == 3) ? 1 : 2;
    int cropX = (s.cropLeft + s.cropRight) * subX;
    int cropY = (s.cropTop + s.cropBottom) * 2 * subY;
    s.croppedWidth = s.frameWidth - cropX;
    s.croppedHeight = s.frameHeight - cropY;

    auto& hist = spsHistory_[s.spsId];
    bool identical = !hist.empty() &&
                     hist.back().data.profileIdc == s.profileIdc &&
                     hist.back().data.levelIdc == s.levelIdc &&
                     hist.back().data.frameWidth == s.frameWidth &&
                     hist.back().data.frameHeight == s.frameHeight;
    if (hist.empty()) {
        hist.push_back({s.spsId, s, nal.offset, nal.offset, 0});
        events_.push_back({EventKind::SpsNew, s.spsId, nal.offset,
                           "SPS id " + std::to_string(s.spsId)});
    } else {
        EventKind kind = identical ? EventKind::SpsDuplicate
                                   : EventKind::SpsUpdate;
        ParamSetRecord<SpsData> rec{
            s.spsId, s, hist.front().firstOffset, nal.offset,
            static_cast<int>(hist.size())};
        events_.push_back({kind, s.spsId, nal.offset,
                           "SPS id " + std::to_string(s.spsId) +
                           (identical ? " duplicated (history retained)"
                                      : " updated mid-stream (history retained)")});
        hist.push_back(std::move(rec));
    }
}

const SpsData* H264Parser::activeSpsForOffset(std::size_t offset) const {
    const SpsData* best = nullptr;
    std::size_t bestOffset = 0;
    for (const auto& kv : spsHistory_)
        for (const auto& rec : kv.second)
            if (rec.lastOffset <= offset &&
                (!best || rec.lastOffset > bestOffset)) {
                best = &rec.data;
                bestOffset = rec.lastOffset;
            }
    return best;
}

const PpsData* H264Parser::activePpsForOffset(std::size_t offset) const {
    const PpsData* best = nullptr;
    std::size_t bestOffset = 0;
    for (const auto& kv : ppsHistory_)
        for (const auto& rec : kv.second)
            if (rec.lastOffset <= offset &&
                (!best || rec.lastOffset > bestOffset)) {
                best = &rec.data;
                bestOffset = rec.lastOffset;
            }
    return best;
}

// PPS and slice bodies are added in the next commit.
void H264Parser::parsePps(const NalUnit&, const std::uint8_t*, bool) {}
void H264Parser::parseSlice(const NalUnit&, const std::uint8_t*, bool) {}

void H264Parser::feed(const NalUnit& nal, const std::uint8_t* streamBase,
                      bool tolerant) {
    switch (nal.nalType) {
        case NAL_SPS: parseSps(nal, streamBase, tolerant); break;
        case NAL_PPS: parsePps(nal, streamBase, tolerant); break;
        case NAL_SLICE:
        case NAL_IDR_SLICE:
            parseSlice(nal, streamBase, tolerant);
            break;
        default:
            break;
    }
}

} // namespace h264
