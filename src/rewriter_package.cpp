#include "rewriter.hpp"
#include "rewriter_detail.hpp"

#include <algorithm>
#include <map>

namespace mp4 {

namespace {

// Original trun data offset of the first trun carrying the field.
int32_t original_data_offset(const TrackFragment& tf,
                             const std::vector<uint8_t>& src) {
    for (const Box* trun : find_children(*tf.traf, "trun")) {
        Cursor c(src, trun->payload_offset(), trun->offset + trun->size);
        uint32_t flags = 0;
        c.fullbox(flags);
        c.u32();  // sample_count
        if (flags & 0x000001) return int32_t(c.u32());
    }
    return 0;
}

void write_traf_shifted(std::vector<uint8_t>& moof,
                        const TrackFragment& tf,
                        const std::vector<uint8_t>& src,
                        uint64_t new_dts, int32_t offset_shift) {
    size_t at = moof.size();
    put_u32(moof, 0);
    put_fourcc(moof, mk4("traf"));
    for (const Box& b : tf.traf->children) {
        if (b.type == "tfhd")
            write_tfhd_box(moof, tf, src, 0 /*base = moof start*/);
        else if (b.type == "tfdt")
            write_tfdt_box(moof, new_dts);
        else if (b.type == "trun")
            write_trun_box(moof, b, src, offset_shift);
        else
            moof.insert(moof.end(), src.begin() + b.offset,
                        src.begin() + b.offset + b.size);
    }
    patch_box_size(moof, at);
}

}  // namespace

RewriteResult rewrite_package(const MediaPackage& pkg,
                              const CompatReport& report,
                              const RewriteOptions& opt) {
    if (!report.ok)
        throw RewriteError("compatibility check failed; refusing to write");
    if (pkg.index.fragments.empty())
        throw RewriteError("no fragments to rewrite");

    std::map<uint32_t, uint32_t> seen;
    for (const Fragment& f : pkg.index.fragments)
        if (++seen[f.sequence_number] > 1)
            throw RewriteError("duplicate moof sequence " +
                               std::to_string(f.sequence_number));

    std::vector<const Fragment*> ordered;
    for (const Fragment& f : pkg.index.fragments) ordered.push_back(&f);
    std::sort(ordered.begin(), ordered.end(),
              [](const Fragment* a, const Fragment* b) {
                  return a->sequence_number < b->sequence_number;
              });

    RewriteResult out;
    out.first_sequence = opt.first_sequence;
    if (!pkg.init) throw RewriteError("initialization segment missing");
    for (const Box& b : pkg.init->boxes)
        write_box(out.init_segment.bytes, b, pkg.init->data);
    out.init_segment.name = opt.init_name;
    if (!verify_output_bytes(out.init_segment.bytes, opt.limits))
        throw RewriteError("rewritten init segment failed verification");

    std::map<uint32_t, uint64_t> next_dts;
    for (const auto& [id, cfg] : pkg.index.tracks) next_dts[id] = 0;
    std::map<uint32_t, uint64_t> max_dev;

    for (size_t fi = 0; fi < ordered.size(); ++fi) {
        const Fragment& f = *ordered[fi];
        const std::vector<uint8_t>& src = f.source->data;
        OutputFile of;
        of.sequence = opt.first_sequence + uint32_t(fi);

        // Per-traf cumulative sample byte offset inside the mdat payload.
        std::vector<uint64_t> sample_offsets;
        uint64_t cursor = 0;
        for (const TrackFragment& tf : f.tracks) {
            sample_offsets.push_back(cursor);
            uint64_t byte_len = 0;
            for (const SampleInfo& s : tf.samples) byte_len += s.size;
            cursor += byte_len;
        }

        // Pass 1: build the moof with zero trun shifts to obtain its size.
        // trun fields are fixed width, so the size here equals the final one.
        std::vector<uint8_t> moof;
        std::vector<uint64_t> expected_dts;
        for (const TrackFragment& tf : f.tracks)
            expected_dts.push_back(next_dts[tf.track_id]);
        {
            std::vector<uint8_t> probe;
            put_u32(probe, 0);
            put_fourcc(probe, mk4("moof"));
            write_mfhd_box(probe, of.sequence);
            for (size_t ti = 0; ti < f.tracks.size(); ++ti)
                write_traf_shifted(probe, f.tracks[ti], src,
                                   expected_dts[ti], 0);
            moof = std::move(probe);
        }
        patch_box_size(moof, 0);
        uint64_t mdat_payload_off = moof.size() + 8;

        // Pass 2: rebuild with the correct data-offset shifts.
        moof.clear();
        put_u32(moof, 0);
        put_fourcc(moof, mk4("moof"));
        write_mfhd_box(moof, of.sequence);
        for (size_t ti = 0; ti < f.tracks.size(); ++ti) {
            const TrackFragment& tf = f.tracks[ti];
            int32_t shift = 0;
            if (tf.trun_flags & 0x000001) {
                int64_t desired =
                    int64_t(mdat_payload_off + sample_offsets[ti]);
                shift = int32_t(desired - original_data_offset(tf, src));
            }
            write_traf_shifted(moof, tf, src, expected_dts[ti], shift);
            uint64_t delta =
                tf.base_media_decode_time >= expected_dts[ti]
                    ? tf.base_media_decode_time - expected_dts[ti]
                    : expected_dts[ti] - tf.base_media_decode_time;
            max_dev[tf.track_id] = std::max(max_dev[tf.track_id], delta);
            next_dts[tf.track_id] =
                checked_add(expected_dts[ti], fragment_duration(tf));
        }
        if (moof.size() != mdat_payload_off - 8)
            throw RewriteError("internal moof sizing error");
        patch_box_size(moof, 0);

        of.bytes = moof;
        uint64_t mdat_size = 8 + f.mdat_payload_size;
        if (mdat_size > UINT32_MAX)
            throw RewriteError("mdat exceeds 32-bit box size");
        put_u32(of.bytes, uint32_t(mdat_size));
        put_fourcc(of.bytes, mk4("mdat"));
        of.bytes.insert(of.bytes.end(),
                        src.begin() + f.mdat_payload_offset,
                        src.begin() + f.mdat_payload_offset +
                            f.mdat_payload_size);

        if (!verify_output_bytes(of.bytes, opt.limits))
            throw RewriteError("rewritten fragment sequence " +
                               std::to_string(of.sequence) +
                               " failed verification");
        of.name = opt.fragment_prefix + pad_sequence(of.sequence) + ".m4s";
        out.fragments.push_back(std::move(of));
    }
    out.max_abs_deviation = max_dev;
    return out;
}

}  // namespace mp4
