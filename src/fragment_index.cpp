#include "fmp4/fragment_index.hpp"

namespace fmp4 {
namespace {

constexpr uint32_t kTfhdKnownFlags = 0x000001 | 0x000002 | 0x000008 | 0x000010 |
                                     0x000020 | 0x010000 | 0x020000;
constexpr uint32_t kTrunKnownFlags = 0x000001 | 0x000004 | 0x000100 | 0x000200 |
                                     0x000400 | 0x000800;

Reader payload_reader(const uint8_t* data, const Box& box,
                      const std::string& file) {
  return Reader(data + box.payload_offset(),
                static_cast<size_t>(box.payload_size()), file,
                box.payload_offset());
}

uint32_t parse_mfhd(const uint8_t* data, const Box& box,
                    const std::string& file) {
  Reader r = payload_reader(data, box, file);
  uint8_t version = r.u8();
  r.u24();
  if (version != 0)
    throw BoxError(file, box.offset,
                   "unsupported mfhd version " + std::to_string(version));
  return r.u32();
}

Tfhd parse_tfhd(const uint8_t* data, const Box& box, const std::string& file) {
  Reader r = payload_reader(data, box, file);
  uint8_t version = r.u8();
  uint32_t flags = r.u24();
  if (version > 1)
    throw BoxError(file, box.offset,
                   "unsupported tfhd version " + std::to_string(version));
  if (flags & ~kTfhdKnownFlags)
    throw BoxError(file, box.offset,
                   "unsupported tfhd flags 0x" + std::to_string(flags));
  Tfhd t;
  t.box_offset = box.offset;
  t.track_id = r.u32();
  if (flags & 0x000001) {
    t.base_data_offset_field = r.abs_pos();
    t.base_data_offset = r.u64();
  }
  if (flags & 0x000002) t.sample_description_index = r.u32();
  if (flags & 0x000008) t.default_sample_duration = r.u32();
  if (flags & 0x000010) t.default_sample_size = r.u32();
  if (flags & 0x000020) t.default_sample_flags = r.u32();
  t.default_base_is_moof = (flags & 0x020000) != 0;
  return t;
}

Tfdt parse_tfdt(const uint8_t* data, const Box& box, const std::string& file) {
  Reader r = payload_reader(data, box, file);
  uint8_t version = r.u8();
  r.u24();
  Tfdt t;
  t.version = version;
  if (version == 0) {
    t.value = r.u32();
  } else if (version == 1) {
    t.value = r.u64();
  } else {
    throw BoxError(file, box.offset,
                   "unsupported tfdt version " + std::to_string(version));
  }
  return t;
}

Trun parse_trun(const uint8_t* data, const Box& box, const std::string& file) {
  Reader r = payload_reader(data, box, file);
  uint8_t version = r.u8();
  uint32_t flags = r.u24();
  if (version > 1)
    throw BoxError(file, box.offset,
                   "unsupported trun version " + std::to_string(version));
  if (flags & ~kTrunKnownFlags)
    throw BoxError(file, box.offset,
                   "unsupported trun flags 0x" + std::to_string(flags));
  Trun t;
  t.version = version;
  t.flags = flags;
  t.box_offset = box.offset;
  uint32_t count = r.u32();
  if (flags & 0x000001) {
    t.data_offset_field = r.abs_pos();
    t.data_offset = r.i32();
  }
  if (flags & 0x000004) t.first_sample_flags = r.u32();
  size_t per_sample = 0;
  if (flags & 0x000100) per_sample += 4;
  if (flags & 0x000200) per_sample += 4;
  if (flags & 0x000400) per_sample += 4;
  if (flags & 0x000800) per_sample += 4;
  if (per_sample > 0 &&
      (uint64_t)count * per_sample > (uint64_t)r.remaining())
    throw BoxError(file, r.abs_pos(), "truncated trun sample table");
  t.samples.reserve(count);
  for (uint32_t i = 0; i < count; ++i) {
    TrunSample s;
    if (flags & 0x000100) s.duration = r.u32();
    if (flags & 0x000200) s.size = r.u32();
    if (flags & 0x000400) s.flags = r.u32();
    if (flags & 0x000800)
      s.cto = version == 1 ? static_cast<int64_t>(r.i32())
                           : static_cast<int64_t>(r.u32());
    t.samples.push_back(s);
  }
  if (r.remaining() != 0)
    throw BoxError(file, r.abs_pos(), "trailing bytes in trun");
  return t;
}

Fragment parse_moof(const uint8_t* data, const Box& moof,
                    const std::string& file) {
  Fragment f;
  f.file = file;
  f.moof_offset = moof.offset;
  f.moof_size = moof.size;
  auto children = parse_children(data, moof, file);
  const Box* mfhd = find_box(children, "mfhd");
  if (!mfhd) throw BoxError(file, moof.offset, "moof without mfhd");
  f.sequence = parse_mfhd(data, *mfhd, file);
  for (const auto& c : children) {
    if (c.type != "traf") continue;
    Traf traf;
    auto tc = parse_children(data, c, file);
    const Box* tfhd = find_box(tc, "tfhd");
    if (!tfhd) throw BoxError(file, c.offset, "traf without tfhd");
    traf.tfhd = parse_tfhd(data, *tfhd, file);
    if (const Box* tfdt = find_box(tc, "tfdt"))
      traf.tfdt = parse_tfdt(data, *tfdt, file);
    for (const auto& x : tc)
      if (x.type == "trun") traf.truns.push_back(parse_trun(data, x, file));
    if (traf.truns.empty()) throw BoxError(file, c.offset, "traf without trun");
    f.trafs.push_back(std::move(traf));
  }
  if (f.trafs.empty()) throw BoxError(file, moof.offset, "moof without traf");
  return f;
}

}  // namespace

SegmentIndex build_segment_index(const std::string& path) {
  auto data = read_file(path);
  auto top = parse_boxes(data.data(), data.size(), path);
  SegmentIndex idx;
  idx.file = path;
  for (size_t i = 0; i < top.size(); ++i) {
    if (top[i].type != "moof") continue;
    Fragment f = parse_moof(data.data(), top[i], path);
    if (i + 1 >= top.size() || top[i + 1].type != "mdat")
      throw BoxError(path, top[i].offset, "moof not followed by mdat");
    f.mdat_offset = top[i + 1].offset;
    f.mdat_size = top[i + 1].size;
    f.mdat_payload_offset = top[i + 1].payload_offset();
    idx.fragments.push_back(std::move(f));
  }
  return idx;
}

uint32_t resolve_sample_duration(const Traf& traf, const TrunSample& sample,
                                 const TrexDefaults* trex,
                                 const std::string& file, uint64_t offset) {
  if (sample.duration) return *sample.duration;
  if (traf.tfhd.default_sample_duration)
    return *traf.tfhd.default_sample_duration;
  if (trex && trex->default_sample_duration != 0)
    return trex->default_sample_duration;
  throw BoxError(file, offset,
                 "no sample duration from trun, tfhd or trex for track " +
                     std::to_string(traf.tfhd.track_id));
}

uint64_t traf_duration(const Traf& traf, const TrexDefaults* trex,
                       const std::string& file) {
  uint64_t total = 0;
  for (const auto& trun : traf.truns)
    for (const auto& s : trun.samples)
      total += resolve_sample_duration(traf, s, trex, file, trun.box_offset);
  return total;
}

}  // namespace fmp4
