#include "slice_header_lists.hpp"

#include <string>
#include <vector>

namespace h264inspect {

namespace {

bool fail(Diagnostic* diagnostic, ErrorKind kind, std::uint64_t offset,
          const std::string& message) {
    if (diagnostic) {
        // The Exp-Golomb decoder classifies its own failures more precisely
        // (truncated vs. over-wide codeword); keep that classification.
        if (diagnostic->kind != ErrorKind::ExpGolomb) {
            diagnostic->kind = kind;
            diagnostic->offset = offset;
            diagnostic->message = message;
        }
    }
    return false;
}

bool read_flag(BitReader& reader, bool& out, Diagnostic* diagnostic) {
    if (!reader.read_bit(out)) {
        if (diagnostic) {
            diagnostic->kind = ErrorKind::Truncated;
            diagnostic->offset = reader.current_offset();
            diagnostic->message = "truncated slice header flag";
        }
        return false;
    }
    return true;
}

}  // namespace

bool skip_ref_pic_list_modification(BitReader& reader, ExpGolombDecoder& decoder,
                                    Diagnostic* diagnostic) {
    std::uint32_t flag = 0;
    if (!reader.read_bits(1, flag)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "truncated ref_pic_list_modification_flag");
    }
    if (flag == 0) return true;
    while (true) {
        std::uint32_t idc = 0;
        if (!decoder.read_ue_u32(idc, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated modification_of_pic_nums_idc");
        }
        if (idc == 3) return true;
        std::uint64_t ignored = 0;
        if (!decoder.read_ue(ignored, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated ref_pic_list_modification value");
        }
    }
}

bool skip_pred_weight_table(BitReader& reader, ExpGolombDecoder& decoder,
                            const Sps& sps, const Pps& pps, std::uint32_t slice_type,
                            Diagnostic* diagnostic) {
    std::uint32_t luma_log2 = 0;
    if (!decoder.read_ue_u32(luma_log2, diagnostic)) {
        return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                    "truncated luma_log2_weight_denom");
    }
    if (sps.ChromaArrayType() != 0) {
        std::uint32_t chroma_log2 = 0;
        if (!decoder.read_ue_u32(chroma_log2, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated chroma_log2_weight_denom");
        }
    }
    struct List {
        std::uint32_t count;
        bool chroma;
    };
    const std::uint32_t chroma = sps.ChromaArrayType();
    const std::uint32_t l0 = pps.num_ref_idx_l0_default_active;
    const std::uint32_t l1 = pps.num_ref_idx_l1_default_active;
    std::vector<List> lists;
    lists.push_back({l0, chroma != 0});
    if (slice_type == static_cast<std::uint32_t>(SliceType::B)) {
        lists.push_back({l1, chroma != 0});
    }
    for (const List& list : lists) {
        for (std::uint32_t i = 0; i < list.count; ++i) {
            bool luma_weight = false;
            if (!read_flag(reader, luma_weight, diagnostic)) return false;
            if (luma_weight) {
                std::int64_t ignored = 0;
                std::uint64_t ignored_ue = 0;
                if (!decoder.read_se(ignored, diagnostic) ||
                    !decoder.read_se(ignored, diagnostic) ||
                    !decoder.read_ue(ignored_ue, diagnostic)) {
                    return fail(diagnostic, ErrorKind::Truncated,
                                reader.current_offset(),
                                "truncated luma weight table entry");
                }
            }
            if (list.chroma) {
                bool chroma_weight = false;
                if (!read_flag(reader, chroma_weight, diagnostic)) return false;
                if (chroma_weight) {
                    for (int c = 0; c < 2; ++c) {
                        std::int64_t ignored = 0;
                        std::uint64_t ignored_ue = 0;
                        if (!decoder.read_se(ignored, diagnostic) ||
                            !decoder.read_se(ignored, diagnostic) ||
                            !decoder.read_ue(ignored_ue, diagnostic)) {
                            return fail(diagnostic, ErrorKind::Truncated,
                                        reader.current_offset(),
                                        "truncated chroma weight table entry");
                        }
                    }
                }
            }
        }
    }
    return true;
}

bool skip_dec_ref_pic_marking(BitReader& reader, ExpGolombDecoder& decoder,
                              bool is_idr, Diagnostic* diagnostic) {
    if (is_idr) {
        bool no_output = false;
        bool long_term = false;
        if (!read_flag(reader, no_output, diagnostic) ||
            !read_flag(reader, long_term, diagnostic)) {
            return false;
        }
        return true;
    }
    bool adaptive = false;
    if (!read_flag(reader, adaptive, diagnostic)) return false;
    if (!adaptive) return true;
    while (true) {
        std::uint32_t op = 0;
        if (!reader.read_bits(1, op)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated memory_management_control_operation");
        }
        std::uint64_t ignored = 0;
        if (!decoder.read_ue(ignored, diagnostic)) {
            return fail(diagnostic, ErrorKind::Truncated, reader.current_offset(),
                        "truncated MMCO operands");
        }
        if (op == 0) return true;
    }
}


}  // namespace h264inspect
