#include "group_index.hpp"

#include <map>

namespace h264 {

namespace {

bool isVcl(std::uint8_t nalType) {
    return nalType == NAL_SLICE || nalType == NAL_IDR_SLICE;
}

} // namespace

GroupIndex buildGroupIndex(const ScanResult& scan,
                           const H264Parser& parser) {
    GroupIndex idx;
    std::map<std::size_t, const SliceHeaderInfo*> shByOffset;
    for (const auto& sh : parser.slices())
        shByOffset.emplace(sh.nalOffset, &sh);

    bool groupOpen = false;
    bool auOpen = false;
    bool boundaryPending = true; // first VCL starts an AU
    int prevFrameNum = -1;
    bool prevIdr = false;
    int prevPoc = -1;
    bool prevWasField = false;

    auto openGroup = [&](const NalUnit& nal) {
        Group g;
        // Byte where the unit framing begins: start code (Annex B) or the
        // 4-byte length prefix (AVCC).
        g.startOffset = nal.offset - (nal.startCodeLen ? nal.startCodeLen : 4);
        g.firstAccessUnit = static_cast<int>(idx.accessUnits.size());
        idx.groups.push_back(g);
        groupOpen = true;
    };

    for (const auto& nal : scan.nals) {
        if (!groupOpen) openGroup(nal);

        if (!isVcl(nal.nalType)) {
            // A non-VCL NAL seen before the first picture marks the group as
            // carrying leading AUD/SEI/parameter-set units. Never downgrade a
            // previously-set flag once VCL data has started.
            if (!auOpen) idx.groups.back().hasLeadingNonVcl = true;
            if (nal.nalType == NAL_SPS || nal.nalType == NAL_PPS) {
                // Parameter-set updates/duplicates are surfaced as events;
                // flag the enclosing group for quick inspection.
                for (const auto& ev : parser.events()) {
                    if (ev.offset == nal.offset &&
                        (ev.kind == EventKind::SpsUpdate ||
                         ev.kind == EventKind::PpsUpdate))
                        idx.groups.back().hasParamSetUpdate = true;
                }
            }
            boundaryPending = true;
            continue;
        }

        auto it = shByOffset.find(nal.offset);
        const SliceHeaderInfo* sh =
            it == shByOffset.end() ? nullptr : it->second;

        bool newPicture = false;
        if (!auOpen || boundaryPending) {
            newPicture = true;
        } else if (sh && sh->parseOk) {
            if (sh->fieldPicFlag) {
                // Same frame_num: a bottom field completes the field pair
                // of the current AU; a top field starts a new picture.
                newPicture = !sh->bottomFieldFlag &&
                             (sh->frameNum != prevFrameNum ||
                              !prevWasField);
            } else if (sh->frameNum != prevFrameNum ||
                       sh->isIdr != prevIdr) {
                newPicture = true;
            } else if (sh->picOrderCntLsb >= 0 &&
                       sh->picOrderCntLsb != prevPoc && !prevWasField) {
                newPicture = true;
            }
            // first_mb_in_slice != 0 => continuation slice of same picture.
        } else if (sh && sh->firstMbInSlice == 0) {
            newPicture = true; // unparsed: fall back to MB boundary
        }

        if (newPicture) {
            if (sh && sh->isIdr && !auOpen) {
                // Group already opened at the leading non-VCL NAL; nothing.
            } else if (sh && sh->isIdr && auOpen) {
                openGroup(nal);
                idx.groups.back().openedByIdr = true;
            }
            AccessUnit au;
            au.index = static_cast<int>(idx.accessUnits.size());
            au.firstNalOffset = nal.offset;
            au.frameNum = sh ? sh->frameNum : -1;
            au.idr = sh && sh->isIdr;
            au.type = sh ? sh->frameType : FT_UNKNOWN;
            au.unknownType = !sh || !sh->parseOk ||
                             sh->frameType == FT_UNKNOWN;
            au.interlaced = sh && sh->fieldPicFlag;
            au.incomplete = !sh || !sh->parseOk;
            idx.accessUnits.push_back(au);
            auOpen = true;
            if (sh && sh->isIdr)
                idx.groups.back().openedByIdr = true;
        }

        if (sh) {
            auto& au = idx.accessUnits.back();
            au.sliceCount++;
            if (sh->fieldPicFlag) au.interlaced = true;
            if (!sh->parseOk) au.incomplete = true;
            if (au.unknownType && sh->parseOk &&
                sh->frameType != FT_UNKNOWN) {
                au.type = sh->frameType;
                au.unknownType = false;
            }
            prevFrameNum = sh->frameNum;
            prevIdr = sh->isIdr;
            prevPoc = sh->picOrderCntLsb;
            prevWasField = sh->fieldPicFlag;
        }
        boundaryPending = false;
    }

    // Roll AUs up into their groups and accumulate frame-type counts.
    for (const auto& au : idx.accessUnits) {
        Group* owner = nullptr;
        for (auto& g : idx.groups)
            if (au.firstNalOffset >= g.startOffset) owner = &g;
        if (!owner) continue;
        owner->frameCount++;
        switch (au.type) {
            case FT_I: owner->types.i++; idx.totals.i++; break;
            case FT_P: owner->types.p++; idx.totals.p++; break;
            case FT_B: owner->types.b++; idx.totals.b++; break;
            default:   owner->types.unknown++; idx.totals.unknown++; break;
        }
        if (au.incomplete) owner->incomplete = true;
    }
    for (auto& g : idx.groups)
        if (g.frameCount == 0) g.incomplete = true;

    idx.skippedNals = static_cast<int>(scan.errors.size());
    for (const auto& e : parser.errors()) {
        idx.skippedNals++;
        if (e.offset < idx.firstErrorOffset)
            idx.firstErrorOffset = e.offset;
    }
    for (const auto& e : scan.errors)
        if (e.offset < idx.firstErrorOffset)
            idx.firstErrorOffset = e.offset;
    return idx;
}

} // namespace h264
