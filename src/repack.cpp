#include "repack.hpp"
#include "box_writer.hpp"

#include <algorithm>
#include <chrono>
#include <filesystem>
#include <fstream>
#include <iomanip>
#include <set>
#include <sstream>

namespace mp4flow {
namespace fs = std::filesystem;

namespace {

// Flat ordered view of every moof: file order then in-file order.
struct OrderedMoof {
  size_t fileIndex;
  size_t moofIndex;
};

std::vector<OrderedMoof> orderedMoofs(const FragmentIndex& idx) {
  std::vector<OrderedMoof> out;
  for (size_t fi = 0; fi < idx.files.size(); ++fi)
    for (size_t mi = 0; mi < idx.files[fi].moofs.size(); ++mi)
      out.push_back({fi, mi});
  return out;
}

const TrackFragment* findTrack(const MoofIndex& m, uint32_t id) {
  for (const auto& t : m.tracks)
    if (t.trackId == id) return &t;
  return nullptr;
}

// Rewrite one moof+mdat pair. Sample payloads are copied verbatim from the
// source mdat; mfhd/tfdt/trun are regenerated from the validated index.
Bytes renderFragment(const ParsedFile& src, const MoofIndex& m,
                     uint32_t newSeq,
                     const std::map<uint32_t, uint64_t>& rebased,
                     const std::vector<uint32_t>& trackOrder) {
  Bytes out;
  BoxWriter w(out);
  size_t moofStart = out.size();
  w.beginBox(fourcc("moof"));
  w.beginFull(fourcc("mfhd"), 0, 0);
  w.u32(newSeq);
  w.endBox();

  // Preserve any non-standard top-level moof boxes verbatim.
  for (const Box& c : m.moof->children)
    if (c.type != fourcc("mfhd") && c.type != fourcc("traf"))
      w.bytes(src.data.data() + c.start, size_t(c.size));

  struct TrunPatch { size_t fieldPos; };
  std::vector<TrunPatch> patches;
  for (uint32_t tid : trackOrder) {
    const TrackFragment* t = findTrack(m, tid);
    w.beginBox(fourcc("traf"));
    w.beginFull(fourcc("tfhd"), 0, 0x020000); // default-base-is-moof
    w.u32(tid);
    w.endBox();
    w.beginFull(fourcc("tfdt"), 1, 0);
    w.u64(rebased.at(tid));
    w.endBox();
    uint32_t flags = 0x000001 | 0x000100 | 0x000200;
    bool ctts = false;
    for (const auto& s : t->samples)
      if (s.compositionOffset != 0) ctts = true;
    if (ctts) flags |= 0x000800;
    w.beginFull(fourcc("trun"), ctts ? 1u : 0u, flags);
    w.u32(uint32_t(t->samples.size()));
    patches.push_back({out.size()});
    w.i32(0); // data_offset patched once mdat position is known
    for (const TrunSample& s : t->samples) {
      w.u32(s.duration);
      w.u32(s.size);
      if (ctts) w.i32(int32_t(s.compositionOffset));
    }
    w.endBox(); // trun
    // Preserve unknown traf siblings.
    const Box* traf = nullptr;
    for (const Box& c : m.moof->children)
      if (c.type == fourcc("traf")) {
        const Box* th = c.findChild(fourcc("tfhd"));
        if (th) {
          Cursor tc(src.data.data(), th->start + th->size,
                    th->payloadOffset(), "moof/traf/tfhd");
          readFull(tc);
          if (tc.u32() == tid) traf = &c;
        }
      }
    if (traf)
      for (const Box& c : traf->children)
        if (c.type != fourcc("tfhd") && c.type != fourcc("tfdt") &&
            c.type != fourcc("trun"))
          w.bytes(src.data.data() + c.start, size_t(c.size));
    w.endBox(); // traf
  }
  w.endBox(); // moof

  w.beginBox(fourcc("mdat"));
  uint64_t mdatDataStart = out.size();
  // Per-track contiguous sample ranges, resolved from the source mdat.
  std::vector<uint64_t> trackOffset;
  for (uint32_t tid : trackOrder) {
    const TrackFragment* t = findTrack(m, tid);
    trackOffset.push_back(mdatDataStart);
    uint64_t srcCursor = t->resolvedDataOffset;
    for (const TrunSample& s : t->samples) {
      w.bytes(src.data.data() + srcCursor, s.size);
      srcCursor += s.size;
    }
  }
  w.endBox(); // mdat

  // Patch trun data offsets relative to the new moof start.
  for (size_t i = 0; i < patches.size(); ++i) {
    int64_t rel = int64_t(trackOffset[i]) - int64_t(moofStart);
    if (rel > int64_t(INT32_MAX) || rel < int64_t(INT32_MIN))
      throw Mp4Error(ErrorKind::LengthOverflow, "moof/traf/trun",
                     "rebuilt data_offset exceeds int32 range");
    int32_t r = int32_t(rel);
    size_t p = patches[i].fieldPos;
    out[p] = uint8_t(uint32_t(r) >> 24);
    out[p + 1] = uint8_t(uint32_t(r) >> 16);
    out[p + 2] = uint8_t(uint32_t(r) >> 8);
    out[p + 3] = uint8_t(r);
  }
  return out;
}

} // namespace

RepackReport repack(const RepackOptions& opt,
                    const std::vector<InitSignature>& inits,
                    const FragmentIndex& idx) {
  std::error_code ec;
  if (!idx.ordinalConflicts.empty()) {
    const SeqConflict& c = idx.ordinalConflicts.front();
    throw Mp4Error(ErrorKind::BadValue, "<media>",
                   "duplicate file ordinal " + std::to_string(c.sequence) +
                       " in " + c.fileA + " and " + c.fileB);
  }
  auto order = orderedMoofs(idx);
  if (order.empty())
    throw Mp4Error(ErrorKind::BadValue, "<media>", "no fragments to repack");

  // Track order follows the first fragment; every track must stay present.
  std::vector<uint32_t> trackOrder;
  for (const auto& t : idx.files[order.front().fileIndex]
                           .moofs[order.front().moofIndex].tracks)
    trackOrder.push_back(t.trackId);
  std::map<uint32_t, uint32_t> timescale;
  for (const auto& sig : inits)
    for (const auto& tr : sig.tracks) timescale[tr.trackId] = tr.timescale;

  // Rebase plan: continuous decode timelines per track.
  std::map<uint32_t, uint64_t> nextTime;
  std::map<uint32_t, int64_t> maxAbsDelta;
  RepackReport rep;
  rep.inputFragments = idx.files.size();
  rep.inputMoofs = order.size();

  struct Job { OrderedMoof where; uint32_t newSeq; Bytes payload;
               std::map<uint32_t, uint64_t> rebased; };
  std::vector<Job> jobs;
  uint32_t newSeq = 1;
  for (const OrderedMoof& om : order) {
    const FragmentFile& ff = idx.files[om.fileIndex];
    const MoofIndex& m = ff.moofs[om.moofIndex];
    std::map<uint32_t, uint64_t> rebased;
    for (uint32_t tid : trackOrder) {
      const TrackFragment* t = findTrack(m, tid);
      if (!t)
        throw Mp4Error(ErrorKind::BadValue, "moof/traf/tfhd",
                       ff.path + ": missing track " + std::to_string(tid));
      uint64_t expected = nextTime[tid];
      int64_t delta = int64_t(t->decodeTime) - int64_t(expected);
      int64_t absd = delta < 0 ? -delta : delta;
      if (absd > maxAbsDelta[tid]) maxAbsDelta[tid] = absd;
      if (opt.maxCorrectionMs && timescale[tid]) {
        int64_t deltaMs = absd * 1000 / int64_t(timescale[tid]);
        if (uint64_t(deltaMs) > opt.maxCorrectionMs)
          throw Mp4Error(ErrorKind::BadValue, "moof/traf/tfdt",
                         ff.path + ": track " + std::to_string(tid) +
                             " correction " + std::to_string(deltaMs) +
                             "ms exceeds --max-correction-ms");
      }
      rebased[tid] = expected;
      nextTime[tid] = checkedAdd(expected, t->totalDuration, "moof/traf/trun");
    }
    jobs.push_back({om, newSeq, Bytes{}, rebased});
    ++newSeq;
  }

  // Render payloads.
  for (Job& job : jobs) {
    const FragmentFile& ff = idx.files[job.where.fileIndex];
    const MoofIndex& m = ff.moofs[job.where.moofIndex];
    job.payload = renderFragment(*ff.parsed, m, job.newSeq, job.rebased,
                                 trackOrder);
  }

  // Stage into a unique sibling directory, validate, then publish atomically.
  fs::path out(opt.outDir);
  fs::path stage = out.parent_path() /
      (".mp4flow-stage-" + std::to_string(
           std::chrono::steady_clock::now().time_since_epoch().count()));
  try {
    fs::remove_all(stage, ec);
    fs::create_directories(stage, ec);
    if (ec) throw Mp4Error(ErrorKind::Io, stage.string(), "cannot create stage");
    for (const Job& job : jobs) {
      std::ostringstream nm;
      nm << std::setw(5) << std::setfill('0') << job.newSeq << "-frag.m4s";
      fs::path p = stage / nm.str();
      std::ofstream f(p, std::ios::binary | std::ios::trunc);
      if (!f) throw Mp4Error(ErrorKind::Io, p.string(), "cannot stage output");
      f.write(reinterpret_cast<const char*>(job.payload.data()),
              std::streamsize(job.payload.size()));
      if (!f) throw Mp4Error(ErrorKind::Io, p.string(), "short stage write");
      rep.outputFiles.push_back(p.string());
    }
    // Full re-parse of every staged file through the same index machinery.
    FragmentIndex staged = buildIndex(stage.string(), inits);
    validateSampleRanges(staged);
    if (staged.conflicts.empty() == false)
      throw Mp4Error(ErrorKind::BadValue, "moof/mfhd",
                     "staged output contains sequence conflicts");
    for (const Job& job : jobs) {
      const MoofIndex* sm = staged.find(job.newSeq);
      if (!sm)
        throw Mp4Error(ErrorKind::BadValue, "moof/mfhd",
                       "staged output missing renumbered sequence");
      for (uint32_t tid : trackOrder) {
        const TrackFragment* t = findTrack(*sm, tid);
        if (!t || t->decodeTime != job.rebased.at(tid))
          throw Mp4Error(ErrorKind::BadValue, "moof/traf/tfdt",
                         "staged tfdt does not match rebase plan");
      }
    }

    fs::remove_all(out, ec);
    fs::rename(stage, out, ec);
    if (ec) {
      // Fallback: copy then remove when rename crosses volumes.
      fs::create_directories(out, ec);
      for (auto& e : fs::directory_iterator(stage))
        fs::copy_file(e.path(), out / e.path().filename(),
                      fs::copy_options::overwrite_existing, ec);
      fs::remove_all(stage, ec);
    }
    rep.outputFiles.clear();
    for (auto& e : fs::directory_iterator(out))
      rep.outputFiles.push_back(e.path().string());
    std::sort(rep.outputFiles.begin(), rep.outputFiles.end());
  } catch (...) {
    std::error_code ignore;
    fs::remove_all(stage, ignore);
    throw;
  }

  rep.outputFragments = rep.outputFiles.size();
  rep.outputMoofs = jobs.size();
  rep.firstOutSeq = 1;
  rep.lastOutSeq = uint32_t(jobs.size());
  rep.continuous = true;
  for (uint32_t tid : trackOrder)
    rep.tracks.push_back({tid, timescale[tid], maxAbsDelta[tid]});
  return rep;
}

} // namespace mp4flow
