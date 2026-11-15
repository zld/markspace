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

void H264Parser::parsePps(const NalUnit& nal, const std::uint8_t* base,
                          bool tolerant) {
    bool truncated = false;
    std::vector<std::uint8_t> rbsp =
        rbspUnescape(base + nal.offset, nal.size, &truncated);
    if (rbsp.size() < 2 || truncated) {
        errors_.push_back({nal.offset, "pps", "truncated RBSP"});
        if (!tolerant) return;
    }
    BitReader br(rbsp.data(), rbsp.size());
    br.readU(8);
    PpsData p;
    p.ppsId = static_cast<int>(decodeUe(br));
    p.spsId = static_cast<int>(decodeUe(br));
    br.readFlag(); // entropy_coding_mode_flag
    br.readFlag(); // bottom_field_pic_order_in_frame_present
    std::uint32_t groups = decodeUe(br);
    if (groups > 0) {
        br.readU(2);
        if (groups > 1) br.readFlag();
        std::uint32_t countMinus1 = groups > 1 ? decodeUe(br) : 0;
        for (std::uint32_t i = 0; i <= countMinus1 && !br.error(); ++i)
            decodeUe(br);
    }
    decodeUe(br); // num_ref_idx_l0_default_active_minus1
    decodeUe(br); // num_ref_idx_l1_default_active_minus1
    p.weightedPredFlag = br.readFlag();
    p.weightedBipredIdc = static_cast<int>(br.readU(2));
    (void)decodeSe(br); // pic_init_qp_minus26
    (void)decodeSe(br); // pic_init_qs_minus26
    (void)decodeSe(br); // chroma_qp_index_offset
    p.deblockingFilterControlPresentFlag = br.readFlag();
    br.readFlag(); // constrained_intra_pred
    p.redundantPicCntPresentFlag = br.readFlag();
    if (br.error()) {
        errors_.push_back({nal.offset, "pps", "truncated or invalid syntax"});
        if (!tolerant) return;
    }
    if (spsHistory_.find(p.spsId) == spsHistory_.end()) {
        events_.push_back({EventKind::PpsMissingSps, p.ppsId, nal.offset,
                           "PPS id " + std::to_string(p.ppsId) +
                           " references unknown SPS id " +
                           std::to_string(p.spsId)});
        errors_.push_back({nal.offset, "pps",
                           "missing referenced SPS id " +
                           std::to_string(p.spsId)});
        if (!tolerant) return;
    }
    auto& hist = ppsHistory_[p.ppsId];
    bool identical = !hist.empty() &&
                     hist.back().data.spsId == p.spsId;
    if (hist.empty()) {
        hist.push_back({p.ppsId, p, nal.offset, nal.offset, 0});
        events_.push_back({EventKind::PpsNew, p.ppsId, nal.offset,
                           "PPS id " + std::to_string(p.ppsId)});
    } else {
        EventKind kind = identical ? EventKind::PpsDuplicate
                                   : EventKind::PpsUpdate;
        ParamSetRecord<PpsData> rec{
            p.ppsId, p, hist.front().firstOffset, nal.offset,
            static_cast<int>(hist.size())};
        events_.push_back({kind, p.ppsId, nal.offset,
                           "PPS id " + std::to_string(p.ppsId) +
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

void H264Parser::parseSlice(const NalUnit& nal, const std::uint8_t* base,
                            bool tolerant) {
    SliceHeaderInfo sh;
    sh.nalOffset = nal.offset;
    sh.nalType = nal.nalType;
    sh.nalRefIdc = nal.nalRefIdc;
    sh.isIdr = nal.nalType == NAL_IDR_SLICE;
    sh.referenced = nal.nalRefIdc != 0;

    bool truncated = false;
    std::vector<std::uint8_t> rbsp =
        rbspUnescape(base + nal.offset, nal.size, &truncated);
    if (rbsp.size() < 2 || truncated) {
        sh.error = "truncated RBSP";
        errors_.push_back({nal.offset, "slice", sh.error});
        slices_.push_back(sh);
        return;
    }
    BitReader br(rbsp.data(), rbsp.size());
    br.readU(8); // NAL header
    sh.firstMbInSlice = static_cast<int>(decodeUe(br));
    sh.sliceTypeRaw = static_cast<int>(decodeUe(br));
    sh.frameType = mapSliceType(sh.sliceTypeRaw > 4
                                    ? sh.sliceTypeRaw - 5
                                    : sh.sliceTypeRaw);
    sh.picParameterSetId = static_cast<int>(decodeUe(br));

    auto ppsIt = ppsHistory_.find(sh.picParameterSetId);
    if (ppsIt == ppsHistory_.end()) {
        sh.error = "slice references unknown PPS id " +
                   std::to_string(sh.picParameterSetId);
        errors_.push_back({nal.offset, "slice", sh.error});
        sh.frameType = FT_UNKNOWN;
        slices_.push_back(sh);
        if (!tolerant) return;
        // Still attempt the remaining fields for diagnostics.
    }
    const PpsData* pps = ppsIt == ppsHistory_.end()
                             ? nullptr : &ppsIt->second.back().data;
    const SpsData* sps = nullptr;
    if (pps) {
        auto it = spsHistory_.find(pps->spsId);
        if (it != spsHistory_.end()) sps = &it->second.back().data;
    }
    if (!sps) {
        sh.error = sh.error.empty()
                       ? "no active SPS for slice header" : sh.error;
        if (sh.error == "no active SPS for slice header")
            errors_.push_back({nal.offset, "slice", sh.error});
        sh.frameType = FT_UNKNOWN;
        slices_.push_back(sh);
        return;
    }

    sh.frameNum = static_cast<int>(br.readU(sps->log2MaxFrameNum));
    if (!sps->frameMbsOnlyFlag)
        sh.fieldPicFlag = br.readFlag();
    if (sh.fieldPicFlag)
        sh.bottomFieldFlag = br.readFlag();
    if (sh.isIdr)
        sh.idrPicId = static_cast<int>(decodeUe(br));
    if (sps->picOrderCntType == 0)
        sh.picOrderCntLsb =
            static_cast<int>(br.readU(sps->log2MaxPicOrderCntLsb));
    else if (sps->picOrderCntType == 1 && !sh.fieldPicFlag) {
        if (br.readFlag())
            sh.deltaPicOrderCntBottom = static_cast<int>(decodeSe(br));
    }
    if (br.error()) {
        sh.error = "truncated slice header";
        errors_.push_back({nal.offset, "slice", sh.error});
        sh.frameType = FT_UNKNOWN;
        slices_.push_back(sh);
        return;
    }
    sh.parseOk = true;
    slices_.push_back(sh);
}

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
            break; // AUD/SEI/filler/end-of-stream: nothing to parse
    }
}

} // namespace h264
