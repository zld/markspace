#include "fmp4/init_compat.hpp"

namespace fmp4 {
namespace {

Reader payload_reader(const uint8_t* data, const Box& box,
                      const std::string& file) {
  return Reader(data + box.payload_offset(),
                static_cast<size_t>(box.payload_size()), file,
                box.payload_offset());
}

uint32_t parse_tkhd_id(const uint8_t* data, const Box& box,
                       const std::string& file) {
  Reader r = payload_reader(data, box, file);
  uint8_t version = r.u8();
  r.u24();
  if (version == 0) {
    r.u32();
    r.u32();
  } else if (version == 1) {
    r.u64();
    r.u64();
  } else {
    throw BoxError(file, box.offset,
                   "unsupported tkhd version " + std::to_string(version));
  }
  return r.u32();
}

uint32_t parse_mdhd_timescale(const uint8_t* data, const Box& box,
                              const std::string& file) {
  Reader r = payload_reader(data, box, file);
  uint8_t version = r.u8();
  r.u24();
  if (version == 0) {
    r.u32();
    r.u32();
  } else if (version == 1) {
    r.u64();
    r.u64();
  } else {
    throw BoxError(file, box.offset,
                   "unsupported mdhd version " + std::to_string(version));
  }
  return r.u32();
}

uint32_t parse_stsd_entries(const uint8_t* data, const Box& box,
                            const std::string& file) {
  Reader r = payload_reader(data, box, file);
  uint8_t version = r.u8();
  r.u24();
  if (version != 0)
    throw BoxError(file, box.offset,
                   "unsupported stsd version " + std::to_string(version));
  uint32_t count = r.u32();
  for (uint32_t i = 0; i < count; ++i) {
    uint64_t entry_off = r.abs_pos();
    uint32_t size = r.u32();
    r.fourcc();
    if (size < 8 || size - 8 > r.remaining())
      throw BoxError(file, entry_off, "invalid stsd sample entry size");
    r.skip(size - 8);
  }
  return count;
}

TrexDefaults parse_trex(const uint8_t* data, const Box& box,
                        const std::string& file) {
  Reader r = payload_reader(data, box, file);
  uint8_t version = r.u8();
  r.u24();
  if (version > 1)
    throw BoxError(file, box.offset,
                   "unsupported trex version " + std::to_string(version));
  TrexDefaults t;
  t.track_id = r.u32();
  t.default_sample_description_index = r.u32();
  t.default_sample_duration = r.u32();
  t.default_sample_size = r.u32();
  t.default_sample_flags = r.u32();
  return t;
}

InitTrack parse_trak(const uint8_t* data, const Box& trak,
                     const std::string& file) {
  InitTrack t;
  auto children = parse_children(data, trak, file);
  const Box* tkhd = find_box(children, "tkhd");
  if (!tkhd) throw BoxError(file, trak.offset, "trak without tkhd");
  t.track_id = parse_tkhd_id(data, *tkhd, file);
  const Box* mdia = find_box(children, "mdia");
  if (!mdia) throw BoxError(file, trak.offset, "trak without mdia");
  auto mdia_children = parse_children(data, *mdia, file);
  const Box* mdhd = find_box(mdia_children, "mdhd");
  if (!mdhd) throw BoxError(file, mdia->offset, "mdia without mdhd");
  t.timescale = parse_mdhd_timescale(data, *mdhd, file);
  const Box* minf = find_box(mdia_children, "minf");
  if (!minf) throw BoxError(file, mdia->offset, "mdia without minf");
  auto minf_children = parse_children(data, *minf, file);
  const Box* stbl = find_box(minf_children, "stbl");
  if (!stbl) throw BoxError(file, minf->offset, "minf without stbl");
  auto stbl_children = parse_children(data, *stbl, file);
  const Box* stsd = find_box(stbl_children, "stsd");
  if (!stsd) throw BoxError(file, stbl->offset, "stbl without stsd");
  t.stsd_entry_count = parse_stsd_entries(data, *stsd, file);
  return t;
}

}  // namespace

InitInfo parse_init(const std::string& path) {
  auto data = read_file(path);
  auto top = parse_boxes(data.data(), data.size(), path);
  const Box* moov = find_box(top, "moov");
  if (!moov) throw BoxError(path, 0, "init segment without moov");
  InitInfo info;
  info.file = path;
  std::vector<TrexDefaults> trexes;
  for (const auto& c : parse_children(data.data(), *moov, path)) {
    if (c.type == "mvex") {
      for (const auto& x : parse_children(data.data(), c, path))
        if (x.type == "trex") trexes.push_back(parse_trex(data.data(), x, path));
    } else if (c.type == "trak") {
      info.tracks.push_back(parse_trak(data.data(), c, path));
    }
  }
  if (info.tracks.empty())
    throw BoxError(path, moov->offset, "init segment without tracks");
  for (auto& t : info.tracks)
    for (const auto& x : trexes)
      if (x.track_id == t.track_id) {
        t.trex = x;
        t.has_trex = true;
      }
  return info;
}

std::vector<InitIssue> validate_init(const InitInfo& init) {
  std::vector<InitIssue> issues;
  for (const auto& t : init.tracks) {
    if (t.timescale == 0)
      issues.push_back({t.track_id, "timescale", "track timescale is zero"});
    if (!t.has_trex)
      issues.push_back({t.track_id, "trex", "no trex defaults for track"});
    if (t.stsd_entry_count == 0)
      issues.push_back({t.track_id, "stsd", "no sample descriptions"});
    if (t.has_trex && (t.trex.default_sample_description_index == 0 ||
                       t.trex.default_sample_description_index >
                           t.stsd_entry_count))
      issues.push_back({t.track_id, "trex.sample_description_index",
                        "trex sample description index out of stsd range"});
  }
  return issues;
}

std::vector<InitIssue> validate_fragments(const InitInfo& init,
                                          const std::vector<Fragment>& frags) {
  std::vector<InitIssue> issues;
  for (const auto& f : frags)
    for (const auto& traf : f.trafs) {
      const InitTrack* t = init.find_track(traf.tfhd.track_id);
      if (!t) {
        issues.push_back({traf.tfhd.track_id, "track_ID",
                          "fragment track not present in init segment"});
        continue;
      }
      if (traf.tfhd.sample_description_index &&
          (*traf.tfhd.sample_description_index == 0 ||
           *traf.tfhd.sample_description_index > t->stsd_entry_count))
        issues.push_back({traf.tfhd.track_id, "stsd.sample_description_index",
                          "tfhd sample description index out of stsd range"});
    }
  return issues;
}

}  // namespace fmp4
