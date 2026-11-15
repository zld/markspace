#ifndef H264_H264_PARSER_HPP
#define H264_H264_PARSER_HPP

#include "nal_scanner.hpp"

#include <cstddef>
#include <cstdint>
#include <map>
#include <string>
#include <vector>

namespace h264 {

enum FrameType { FT_P = 0, FT_B = 1, FT_I = 2, FT_UNKNOWN = 3 };

const char* frameTypeName(FrameType t);

struct SpsData {
    int spsId = -1;
    int profileIdc = 0;
    int constraintFlags = 0;
    int levelIdc = 0;
    int chromaFormatIdc = 1;
    int log2MaxFrameNum = 4;
    int picOrderCntType = 0;
    int log2MaxPicOrderCntLsb = 0;
    int maxNumRefFrames = 0;
    int picWidthInMbs = 0;
    int picHeightInMapUnits = 0;
    bool frameMbsOnlyFlag = true;
    bool mbAdaptiveFrameFieldFlag = false;
    bool direct8x8InferenceFlag = false;
    bool frameCroppingFlag = false;
    int cropLeft = 0, cropRight = 0, cropTop = 0, cropBottom = 0;
    // VUI timing
    bool vuiPresent = false;
    bool timingInfoPresent = false;
    std::uint32_t numUnitsInTick = 0;
    std::uint32_t timeScale = 0;
    bool fixedFrameRateFlag = false;
    // Derived dimensions in pixels (luma samples).
    int widthMbUnits = 0;       // pic_width_in_mbs_minus1 + 1
    int heightMapUnits = 0;     // pic_height_in_map_units_minus1 + 1
    int frameWidth = 0;         // uncropped
    int frameHeight = 0;        // uncropped, doubled when field coding allowed
    int croppedWidth = 0;
    int croppedHeight = 0;
    double frameRate = 0.0;     // 0 when unknown
};

struct PpsData {
    int ppsId = -1;
    int spsId = -1;
    bool redundantPicCntPresentFlag = false;
    bool weightedPredFlag = false;
    int weightedBipredIdc = 0;
    bool deblockingFilterControlPresentFlag = true;
};

template <typename T>
struct ParamSetRecord {
    int id = -1;
    T data;
    std::size_t firstOffset = 0;
    std::size_t lastOffset = 0;
    int version = 0;            // 0 = first, >0 = mid-stream update
};

struct SliceHeaderInfo {
    std::size_t nalOffset = 0;  // join key with NalUnit.offset
    std::uint8_t nalType = 0;
    std::uint8_t nalRefIdc = 0;
    bool isIdr = false;
    int firstMbInSlice = -1;
    int sliceTypeRaw = -1;      // raw syntax value, -1 if unparsed
    FrameType frameType = FT_UNKNOWN;
    int picParameterSetId = -1;
    int frameNum = -1;
    bool fieldPicFlag = false;
    bool bottomFieldFlag = false;
    int idrPicId = -1;
    int picOrderCntLsb = -1;
    int deltaPicOrderCntBottom = 0;
    int deltaPicOrderCnt[2] = {0, 0};
    bool referenced = false;    // nal_ref_idc != 0
    bool parseOk = false;
    std::string error;
};

enum class EventKind {
    SpsNew, SpsDuplicate, SpsUpdate,
    PpsNew, PpsDuplicate, PpsUpdate, PpsMissingSps,
};

struct ParamSetEvent {
    EventKind kind;
    int id = -1;
    std::size_t offset = 0;
    std::string detail;
};

struct ParseError {
    std::size_t offset = 0;
    std::string stage;
    std::string reason;
};

class H264Parser {
public:
    // Parse one NAL unit. streamBase points at the full input buffer so
    // payload offsets can be resolved. In tolerant mode failures are
    // recorded; in strict mode the caller stops at the first failure.
    void feed(const NalUnit& nal, const std::uint8_t* streamBase,
              bool tolerant);

    const std::map<int, std::vector<ParamSetRecord<SpsData>>>& spsHistory() const {
        return spsHistory_;
    }
    const std::map<int, std::vector<ParamSetRecord<PpsData>>>& ppsHistory() const {
        return ppsHistory_;
    }
    const std::vector<SliceHeaderInfo>& slices() const { return slices_; }
    const std::vector<ParamSetEvent>& events() const { return events_; }
    const std::vector<ParseError>& errors() const { return errors_; }

    const SpsData* activeSpsForOffset(std::size_t offset) const;
    const PpsData* activePpsForOffset(std::size_t offset) const;

private:
    void parseSps(const NalUnit& nal, const std::uint8_t* base, bool tolerant);
    void parsePps(const NalUnit& nal, const std::uint8_t* base, bool tolerant);
    void parseSlice(const NalUnit& nal, const std::uint8_t* base, bool tolerant);

    std::map<int, std::vector<ParamSetRecord<SpsData>>> spsHistory_;
    std::map<int, std::vector<ParamSetRecord<PpsData>>> ppsHistory_;
    std::vector<SliceHeaderInfo> slices_;
    std::vector<ParamSetEvent> events_;
    std::vector<ParseError> errors_;
};

FrameType mapSliceType(int rawSyntaxValue);

} // namespace h264

#endif
