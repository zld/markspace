#include "fragment_index.hpp"

#include <algorithm>
#include <cctype>
#include <filesystem>
#include <limits>
#include <sstream>

namespace mp4flow {
namespace fs = std::filesystem;

const MoofIndex* FragmentIndex::find(uint32_t seq) const {
  auto it = bySeq.find(seq);
  if (it == bySeq.end()) return nullptr;
  return &files[it->second.fileIndex].moofs[it->second.moofIndex];
}

std::vector<uint32_t>
FragmentIndex::missingSequences(uint32_t first, uint32_t last) const {
  std::vector<uint32_t> out;
  if (last < first) return out;
  for (uint64_t s = first; s <= uint64_t(last); ++s)
    if (!bySeq.count(uint32_t(s))) out.push_back(uint32_t(s));
  return out;
}

namespace {

struct TrexDefaults { uint32_t duration = 0, size = 0, flags = 0; };

std::map<uint32_t, TrexDefaults>
trexTable(const std::vector<InitSignature>& inits) {
  std::map<uint32_t, TrexDefaults> t;
  for (const auto& sig : inits)
    for (const auto& tr : sig.tracks)
      t[tr.trackId] = {tr.trexDefDuration, tr.trexDefSize, tr.trexDefFlags};
  return t;
}

TrackFragment parseTraf(const ParsedFile& pf, const Box& traf,
                        const std::map<uint32_t, TrexDefaults>& trex,
                        bool& baseIsMoof, uint64_t& explicitBase,
                        bool& haveExplicitBase) {
  TrackFragment tf;
  uint32_t tfhdDefDur = 0, tfhdDefSize = 0;
  bool haveTfhdDur = false, haveTfhdSize = false;
  for (const Box& b : traf.children) {
    std::string bp = "moof/traf/" + typeName(b.type);
    Cursor c(pf.data.data(), b.start + b.size, b.payloadOffset(), bp);
    if (b.type == fourcc("tfhd")) {
      FullHeader fh = readFull(c);
      tf.trackId = c.u32();
      if (fh.flags & 0x000001) { explicitBase = c.u64(); haveExplicitBase = true; }
      if (fh.flags & 0x000002) { tfhdDefDur = c.u32(); haveTfhdDur = true; }
      if (fh.flags & 0x000004) { tfhdDefSize = c.u32(); haveTfhdSize = true; }
      if (fh.flags & 0x000008) c.u32(); // default_sample_flags
      baseIsMoof = (fh.flags & 0x010000) != 0; // default-base-is-moof
      auto it = trex.find(tf.trackId);
      if (it == trex.end())
        throw Mp4Error(ErrorKind::BadValue, bp,
                       "track id has no matching trex in init segments");
      const TrexDefaults& td = it->second;
      tf.defaultDuration = haveTfhdDur ? tfhdDefDur : td.duration;
      tf.defaultSize = haveTfhdSize ? tfhdDefSize : td.size;
    } else if (b.type == fourcc("tfdt")) {
      FullHeader fh = readFull(c);
      if (fh.version == 1) tf.decodeTime = c.u64();
      else if (fh.version == 0) tf.decodeTime = c.u32();
      else throw Mp4Error(ErrorKind::BadValue, bp, "tfdt version 0 or 1 only");
      tf.tfdtPresent = true;
    } else if (b.type == fourcc("trun")) {
      FullHeader fh = readFull(c);
      if (fh.version > 1)
        throw Mp4Error(ErrorKind::BadValue, bp, "trun version 0 or 1 only");
      uint32_t count = c.u32();
      tf.sampleCount = count;
      if (fh.flags & 0x000001) {
        tf.dataOffset = int64_t(c.i32());
        tf.haveTrunOffset = true;
      }
      if (fh.flags & 0x000004) c.u32(); // first_sample_flags
      tf.samples.resize(count);
      for (uint32_t i = 0; i < count; ++i) {
        TrunSample& s = tf.samples[i];
        if (fh.flags & 0x000100) s.duration = c.u32();
        if (fh.flags & 0x000200) s.size = c.u32();
        if (fh.flags & 0x000400) c.u32(); // per-sample flags
        if (fh.flags & 0x000800)
          s.compositionOffset = fh.version == 1 ? int64_t(c.i32())
                                                : int64_t(c.u32());
      }
    }
  }
  if (tf.trackId == 0)
    throw Mp4Error(ErrorKind::BadValue, "moof/traf/tfhd", "traf has no tfhd");
  // Resolve trun -> tfhd/trex priority and totals.
  for (TrunSample& s : tf.samples) {
    if (!s.duration) s.duration = tf.defaultDuration;
    if (!s.size) s.size = tf.defaultSize;
    tf.totalDuration = checkedAdd(tf.totalDuration, s.duration, "moof/traf/trun");
  }
  return tf;
}

} // namespace

FragmentIndex buildIndex(const std::string& dir,
                         const std::vector<InitSignature>& inits) {
  auto trex = trexTable(inits);
  FragmentIndex idx;
  std::vector<fs::directory_entry> entries;
  std::error_code ec;
  if (!fs::exists(dir, ec) || !fs::is_directory(dir, ec))
    throw Mp4Error(ErrorKind::Io, dir, "media directory missing");
  for (const auto& e : fs::directory_iterator(dir, ec))
    if (e.is_regular_file(ec)) entries.push_back(e);
  std::sort(entries.begin(), entries.end(),
            [](const fs::directory_entry& a, const fs::directory_entry& b) {
              return a.path().filename().string() < b.path().filename().string();
            });

  for (const auto& e : entries) {
    std::string name = e.path().filename().string();
    size_t n = 0;
    while (n < name.size() && std::isdigit(uint8_t(name[n]))) ++n;
    if (n == 0) continue; // non-fragment file, ignore
    FragmentFile ff;
    ff.path = e.path().string();
    ff.ordinal = std::stoi(name.substr(0, n));
    ff.parsed = std::make_shared<ParsedFile>(parseFile(ff.path));
    const ParsedFile& pf = *ff.parsed;

    for (size_t i = 0; i < pf.boxes.size(); ++i) {
      const Box& box = pf.boxes[i];
      if (box.type != fourcc("moof")) continue;
      MoofIndex mi;
      mi.moof = &box;
      mi.moofEnd = box.start + box.size;
      const Box* mfhd = box.findChild(fourcc("mfhd"));
      if (!mfhd)
        throw Mp4Error(ErrorKind::BadValue, "moof/mfhd", "moof has no mfhd");
      mi.sequence = [&] {
        Cursor c(pf.data.data(), mfhd->start + mfhd->size,
                 mfhd->payloadOffset(), "moof/mfhd");
        FullHeader fh = readFull(c);
        if (fh.version != 0 || fh.flags != 0)
          throw Mp4Error(ErrorKind::BadValue, c.path, "mfhd v0 flags 0 only");
        return c.u32();
      }();
      bool baseIsMoof = false, haveBase = false;
      uint64_t explicitBase = 0;
      for (const Box& traf : box.children)
        if (traf.type == fourcc("traf"))
          mi.tracks.push_back(parseTraf(pf, traf, trex, baseIsMoof,
                                        explicitBase, haveBase));
      if (mi.tracks.empty())
        throw Mp4Error(ErrorKind::BadValue, "moof/traf", "moof has no traf");
      // Pair with the next top-level mdat.
      const Box* mdat = (i + 1 < pf.boxes.size() &&
                         pf.boxes[i + 1].type == fourcc("mdat"))
                            ? &pf.boxes[i + 1]
                            : nullptr;
      if (!mdat)
        throw Mp4Error(ErrorKind::IllegalNesting, "moof/mdat",
                       "moof not followed by mdat in same file");
      mi.mdat = mdat;
      mi.mdatRange = {mdat->payloadOffset(), mdat->payloadSize()};
      uint64_t base = haveBase ? explicitBase
                               : (baseIsMoof ? box.start : 0);
      for (TrackFragment& tf : mi.tracks) {
        if (!tf.haveTrunOffset) { tf.resolvedDataOffset = base; continue; }
        int64_t signedBase = int64_t(base);
        if (base > uint64_t(std::numeric_limits<int64_t>::max()))
          throw Mp4Error(ErrorKind::LengthOverflow, "moof/traf/trun",
                         "base offset too large for signed data_offset");
        int64_t resolved = signedBase + tf.dataOffset;
        if (resolved < 0)
          throw Mp4Error(ErrorKind::BadValue, "moof/traf/trun",
                         "data_offset resolves before file start");
        tf.resolvedDataOffset = uint64_t(resolved);
      }
      ff.moofs.push_back(std::move(mi));
    }
    idx.files.push_back(std::move(ff));
  }

  std::sort(idx.files.begin(), idx.files.end(),
            [](const FragmentFile& a, const FragmentFile& b) {
              if (a.ordinal != b.ordinal) return a.ordinal < b.ordinal;
              return a.path < b.path;
            });
  for (size_t fi = 0; fi < idx.files.size(); ++fi) {
    const FragmentFile& f = idx.files[fi];
    for (size_t mi = 0; mi < f.moofs.size(); ++mi) {
      uint32_t seq = f.moofs[mi].sequence;
      auto [it, inserted] = idx.bySeq.emplace(seq, MoofRef{fi, mi});
      if (!inserted) {
        const MoofRef& prior = it->second;
        idx.conflicts.push_back(
            {seq, idx.files[prior.fileIndex].path, f.path});
      }
    }
  }
  return idx;
}

void validateSampleRanges(const FragmentIndex& idx) {
  for (const FragmentFile& f : idx.files) {
    for (const MoofIndex& m : f.moofs) {
      for (const TrackFragment& tf : m.tracks) {
        uint64_t cursor = tf.resolvedDataOffset;
        if (cursor < m.mdatRange.offset ||
            cursor > m.mdatRange.offset + m.mdatRange.size)
          throw Mp4Error(ErrorKind::BadValue, "moof/traf/trun",
                         f.path + ": data_offset outside paired mdat");
        for (const TrunSample& s : tf.samples) {
          uint64_t end = checkedAdd(cursor, s.size, "moof/traf/trun");
          if (end > m.mdatRange.offset + m.mdatRange.size)
            throw Mp4Error(ErrorKind::Truncated, "moof/traf/trun",
                           f.path + ": sample data exceeds mdat range");
          cursor = end;
        }
      }
    }
  }
}

} // namespace mp4flow
