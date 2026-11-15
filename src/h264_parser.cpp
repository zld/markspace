#include "h264_parser.hpp"
#include "bit_reader.hpp"
#include "exp_golomb.hpp"

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

// Parsing bodies are added in follow-up commits (SPS, then PPS/slice).
void H264Parser::parseSps(const NalUnit&, const std::uint8_t*, bool) {}
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
