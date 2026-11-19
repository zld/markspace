#ifndef H264_ACCESS_UNIT_HPP
#define H264_ACCESS_UNIT_HPP

#include <cstdint>
#include <vector>

#include "diagnostics.hpp"
#include "nal_parser.hpp"
#include "parameter_sets.hpp"
#include "slice_parser.hpp"

namespace h264 {

// One decoded access unit (a picture). A frame picture has one or more
// slices; a field picture is a single field (two complementary fields with
// the same frame_num are reported as two separate field pictures).
struct PictureInfo {
    std::uint64_t start_offset = 0;   // file offset of the picture's first NAL
    unsigned frame_num = 0;
    FrameKind kind = FrameKind::Unknown;
    bool is_idr = false;
    bool is_field = false;
    bool bottom_field = false;
    unsigned slice_count = 0;
    unsigned pps_id = 0;
    bool parse_failed = false;        // header error kept for the summary
};

// A group of pictures starting at an IDR picture. `closed` is false for the
// final group when no later IDR terminates it before EOF.
struct GopInfo {
    std::uint64_t start_offset = 0;   // first NAL offset of the IDR picture
    std::size_t first_picture = 0;    // index into the picture vector
    std::size_t picture_count = 0;
    bool closed = false;
};

struct FrameStats {
    std::size_t i = 0, p = 0, b = 0, unknown = 0;
    std::size_t frame_pics = 0;
    std::size_t field_pics = 0;

    std::size_t total() const noexcept { return i + p + b + unknown; }
};

// Stateful assembler. Feed every NAL in file order; non-VCL NALs are
// ignored here (parameter sets are handled by the caller), VCL NALs are
// grouped into pictures and GOPs.
class AccessUnitAssembler {
public:
    explicit AccessUnitAssembler(const ParameterSetStore& store)
        : store_(store) {}

    void handle_nal(const NalUnit& nal, Diagnostics& diag);

    // Flush the picture still open at EOF and close GOP bookkeeping.
    void finish(Diagnostics& diag);

    const std::vector<PictureInfo>& pictures() const noexcept { return pictures_; }
    const std::vector<GopInfo>& groups() const noexcept { return groups_; }
    const FrameStats& stats() const noexcept { return stats_; }

    // Pictures that precede the first IDR (no group owns them).
    const std::vector<PictureInfo>& leading_pictures() const noexcept {
        return leading_;
    }

private:
    void flush_picture();
    void start_gop_for(const PictureInfo& pic);
    static bool same_picture(const PictureInfo& cur, const SliceHeader& sh);

    const ParameterSetStore& store_;
    std::vector<PictureInfo> pictures_;
    std::vector<PictureInfo> leading_;
    std::vector<GopInfo> groups_;
    FrameStats stats_;

    bool have_open_ = false;
    PictureInfo open_;
    SliceHeader open_header_;
};

}  // namespace h264

#endif
