#include "fragment_index.hpp"

#include <algorithm>
#include <filesystem>
#include <set>

namespace mp4 {

namespace fs = std::filesystem;

namespace {

const Box* require_child(const Box& parent, const char* type) {
    const Box* b = find_child(parent, type);
    if (!b) throw IndexError(std::string("missing required box ") + type +
                             " inside " + parent.type.str());
    return b;
}

// tfhd flags, ISO 14496-12 8.8.7.
constexpr uint32_t TFHD_BASE_DATA_OFFSET = 0x000001;
constexpr uint32_t TFHD_DEFAULT_DURATION = 0x000002;
constexpr uint32_t TFHD_DEFAULT_SIZE = 0x000008;
constexpr uint32_t TFHD_DEFAULT_FLAGS = 0x000020;
constexpr uint32_t TFHD_DURATION_IS_EMPTY = 0x010000;
constexpr uint32_t TFHD_DEFAULT_BASE_IS_MOOF = 0x020000;

// trun flags, ISO 14496-12 8.8.8.
constexpr uint32_t TRUN_DATA_OFFSET = 0x000001;
constexpr uint32_t TRUN_FIRST_FLAG = 0x000004;
constexpr uint32_t TRUN_DURATION = 0x000100;
constexpr uint32_t TRUN_SIZE = 0x000200;
constexpr uint32_t TRUN_FLAGS = 0x000400;
constexpr uint32_t TRUN_C_OFFSET = 0x000800;

TrackFragment parse_traf(const std::vector<uint8_t>& data, const Box& traf,
                         uint64_t moof_offset, const TrackConfig& cfg) {
    TrackFragment tf;
    tf.traf = &traf;

    const Box& tfhd = *require_child(traf, "tfhd");
    Cursor hc(data, tfhd.payload_offset(), tfhd.offset + tfhd.size);
    uint8_t hver = hc.fullbox(tf.tfhd_flags);
    (void)hver;
    tf.track_id = hc.u32();
    if (tf.track_id != cfg.track_id)
        throw IndexError("tfhd track id " + std::to_string(tf.track_id) +
                         " not present in initialization segment");
    tf.base_data_offset = moof_offset;  // default-base-is-moof default
    tf.default_sample_duration = cfg.default_sample_duration;
    tf.default_sample_size = cfg.default_sample_size;
    tf.default_sample_flags = cfg.default_sample_flags;
    if (tf.tfhd_flags & TFHD_BASE_DATA_OFFSET)
        tf.base_data_offset = hc.u64();
    if (tf.tfhd_flags & TFHD_DEFAULT_DURATION)
        tf.default_sample_duration = hc.u32();
    if (tf.tfhd_flags & TFHD_DEFAULT_SIZE)
        tf.default_sample_size = hc.u32();
    if (tf.tfhd_flags & TFHD_DEFAULT_FLAGS)
        tf.default_sample_flags = hc.u32();
    if (tf.tfhd_flags & TFHD_DEFAULT_BASE_IS_MOOF)
        tf.base_data_offset = moof_offset;
    if (tf.tfhd_flags & TFHD_DURATION_IS_EMPTY)
        return tf;  // no samples are carried by this traf

    if (const Box* tfdt = find_child(traf, "tfdt")) {
        Cursor dc(data, tfdt->payload_offset(), tfdt->offset + tfdt->size);
        uint32_t dflags = 0;
        tf.tfdt_version = dc.fullbox(dflags);
        tf.base_media_decode_time =
            tf.tfdt_version == 1 ? dc.u64() : dc.u32();
    }

    for (const Box* trunb : find_children(traf, "trun")) {
        Cursor tc(data, trunb->payload_offset(), trunb->offset + trunb->size);
        uint32_t tflags = 0;
        tf.trun_version = tc.fullbox(tflags);
        tf.trun_flags |= tflags;
        uint32_t count = tc.u32();
        if (tflags & TRUN_DATA_OFFSET) tf.data_offset = int32_t(tc.u32());
        if (tflags & TRUN_FIRST_FLAG) tc.u32();  // first_sample_flags
        for (uint32_t i = 0; i < count; ++i) {
            SampleInfo s;
            s.duration = uint32_t(tf.default_sample_duration);
            s.size = tf.default_sample_size;
            s.flags = tf.default_sample_flags;
            if (tflags & TRUN_DURATION) s.duration = tc.u32();
            if (tflags & TRUN_SIZE) s.size = tc.u32();
            if (tflags & TRUN_FLAGS) s.flags = tc.u32();
            if (tflags & TRUN_C_OFFSET)
                s.composition_offset =
                    tf.trun_version == 1 ? int32_t(tc.u32())
                                         : int32_t(int16_t(uint16_t(tc.u32())));
            tf.samples.push_back(s);
        }
    }
    return tf;
}

void extend_timeline(TrackTimeline& tl, TrackFragment& tf,
                     uint32_t sequence, const Limits& lim) {
    uint64_t dts = tf.base_media_decode_time;
    if (dts < tl.next_decode_time)
        tl.issues.push_back({TimelineIssue::Overlap, sequence,
                             tl.next_decode_time, dts});
    else if (dts > tl.next_decode_time)
        tl.issues.push_back({TimelineIssue::Gap, sequence,
                             tl.next_decode_time, dts});

    uint64_t cursor = dts;
    for (SampleInfo& s : tf.samples) {
        s.decode_time = cursor;
        cursor = checked_add(cursor, s.duration);
        if (++tl.sample_count > lim.max_samples)
            throw IndexError("sample count exceeds configured maximum");
    }
    // Fragments may arrive out of order; the timeline frontier is the
    // furthest sample end time observed so far.
    if (cursor > tl.next_decode_time) tl.next_decode_time = cursor;
    tl.total_duration = tl.next_decode_time;
}

}  // namespace

void index_media_segment(MediaIndex& idx, const MediaFile& segment,
                         const Limits& lim) {
    const std::string& file = segment.path;
    const std::vector<uint8_t>& data = segment.data;
    // Index the box tree owned by the MediaFile so every Box* stored in the
    // index stays valid for the package's lifetime (no dangling locals).
    const std::vector<Box>& boxes = segment.boxes;

    for (size_t i = 0; i < boxes.size(); ++i) {
        const Box& moof = boxes[i];
        if (moof.type != "moof") continue;

        const Box* mfhd = find_child(moof, "mfhd");
        if (!mfhd) throw IndexError("moof without mfhd in " + file);
        Cursor mc(data, mfhd->payload_offset(), mfhd->offset + mfhd->size);
        uint32_t mflags = 0;
        mc.fullbox(mflags);
        uint32_t sequence = mc.u32();

        Fragment frag;
        frag.file = file;
        frag.source = &segment;
        frag.moof = &moof;
        frag.sequence_number = sequence;

        std::vector<const Box*> trafs = find_children(moof, "traf");
        if (trafs.empty()) throw IndexError("moof without traf in " + file);
        for (const Box* traf : trafs) {
            const Box* tfhd = require_child(*traf, "tfhd");
            Cursor hc(data, tfhd->payload_offset(),
                      tfhd->offset + tfhd->size);
            uint32_t hflags = 0;
            hc.fullbox(hflags);
            uint32_t track_id = hc.u32();
            auto cfg_it = idx.tracks.find(track_id);
            if (cfg_it == idx.tracks.end())
                throw IndexError(
                    "fragment references track " + std::to_string(track_id) +
                    " missing from initialization segment");
            TrackFragment tf = parse_traf(data, *traf, moof.offset,
                                          cfg_it->second);
            frag.tracks.push_back(tf);
        }

        const Box* mdat = (i + 1 < boxes.size() &&
                           boxes[i + 1].type == "mdat")
                              ? &boxes[i + 1]
                              : nullptr;
        if (!mdat)
            throw IndexError("moof sequence " + std::to_string(sequence) +
                             " has no following mdat in " + file);
        frag.mdat_payload_offset = mdat->payload_offset();
        frag.mdat_payload_size = mdat->payload_size();

        uint64_t declared = 0;
        for (TrackFragment& tf : frag.tracks) {
            for (SampleInfo& s : tf.samples)
                declared = checked_add(declared, s.size);
        }
        if (declared != frag.mdat_payload_size)
            throw IndexError(
                "sample data length mismatch in moof sequence " +
                std::to_string(sequence) + ": trun declares " +
                std::to_string(declared) + " bytes, mdat holds " +
                std::to_string(frag.mdat_payload_size));

        for (TrackFragment& tf : frag.tracks) {
            if (tf.samples.empty()) continue;
            if (tf.data_offset == 0)
                throw IndexError("trun data-offset missing for track " +
                                 std::to_string(tf.track_id));
            int64_t begin =
                int64_t(tf.base_data_offset) + int64_t(tf.data_offset);
            uint64_t first_sample = 0;
            for (const TrackFragment& other : frag.tracks) {
                if (&other == &tf) break;
                for (const SampleInfo& s : other.samples)
                    first_sample = checked_add(first_sample, s.size);
            }
            int64_t expected = int64_t(frag.mdat_payload_offset) +
                               int64_t(first_sample);
            if (begin != expected)
                throw IndexError(
                    "trun data offset does not point into mdat for track " +
                    std::to_string(tf.track_id));
            TrackTimeline& tl = idx.timelines[tf.track_id];
            extend_timeline(tl, tf, sequence, lim);
        }
        idx.fragments.push_back(frag);
    }
}

ScanResult scan_input_dir(const std::string& dir) {
    if (!fs::exists(dir) || !fs::is_directory(dir))
        throw FileError("input directory not found: " + dir);
    static const std::set<std::string> exts = {".mp4", ".m4s", ".m4v",
                                               ".bin"};
    ScanResult res;
    for (const auto& e : fs::directory_iterator(dir)) {
        if (!e.is_regular_file() ||
            !exts.count(e.path().extension().string()))
            continue;
        res.segment_paths.push_back(e.path().string());
    }
    std::sort(res.segment_paths.begin(), res.segment_paths.end());
    return res;
}

MediaPackage load_package(const std::string& dir, const Limits& lim) {
    ScanResult scan = scan_input_dir(dir);
    MediaPackage pkg;
    std::vector<std::pair<std::string, std::vector<uint8_t>>> media;

    for (const std::string& path : scan.segment_paths) {
        std::vector<uint8_t> buf = read_file(path, {lim.max_file_size});
        std::vector<Box> boxes = parse_boxes(buf, {lim.max_file_size});
        auto file = std::make_unique<MediaFile>();
        file->path = path;
        file->data = std::move(buf);
        file->boxes = std::move(boxes);
        if (find_box(file->boxes, "moov")) {
            if (pkg.init)
                throw IndexError("multiple initialization segments in " + dir);
            file->is_init = true;
            pkg.init = std::move(file);
        } else {
            pkg.segments.push_back(std::move(file));
        }
    }
    if (!pkg.init)
        throw IndexError("initialization segment (moov) not found in " + dir);

    pkg.index = parse_init_segment(pkg.init->data, lim);
    if (pkg.segments.empty())
        throw IndexError("no media fragments found in " + dir);
    for (const auto& seg : pkg.segments)
        index_media_segment(pkg.index, *seg, lim);
    return pkg;
}

}  // namespace mp4
