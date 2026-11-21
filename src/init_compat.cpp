#include "init_compat.hpp"

#include <sstream>

namespace mp4flow {

namespace {

std::string readHandlerType(const ParsedFile& pf, const Box& hdlr) {
  Cursor c(pf.data.data(), hdlr.start + hdlr.size, hdlr.payloadOffset(),
           "moov/trak/mdia/hdlr");
  FullHeader fh = readFull(c);
  if (fh.version != 0)
    throw Mp4Error(ErrorKind::BadValue, c.path, "hdlr version must be 0");
  c.u32();               // pre_defined
  uint32_t type = c.u32(); // handler_type
  return typeName(type);
}

uint32_t readTimescale(const ParsedFile& pf, const Box& mdhd) {
  Cursor c(pf.data.data(), mdhd.start + mdhd.size, mdhd.payloadOffset(),
           "moov/trak/mdia/mdhd");
  FullHeader fh = readFull(c);
  if (fh.version == 1) {
    c.u64(); // creation_time
    c.u64(); // modification_time
  } else if (fh.version == 0) {
    c.u32();
    c.u32();
  } else {
    throw Mp4Error(ErrorKind::BadValue, c.path, "mdhd version must be 0 or 1");
  }
  uint32_t ts = c.u32();
  if (ts == 0)
    throw Mp4Error(ErrorKind::BadValue, c.path, "timescale must be non-zero");
  return ts;
}

void readTrex(const ParsedFile& pf, const Box& trex, TrackSignature& sig) {
  Cursor c(pf.data.data(), trex.start + trex.size, trex.payloadOffset(),
           "moov/mvex/trex");
  FullHeader fh = readFull(c);
  if (fh.version != 0 || fh.flags != 0)
    throw Mp4Error(ErrorKind::BadValue, c.path, "trex must be version 0 flags 0");
  c.u32(); // track_id, cross-checked separately
  sig.trexDescIndex = c.u32();
  sig.trexDefDuration = c.u32();
  sig.trexDefSize = c.u32();
  sig.trexDefFlags = c.u32();
}

} // namespace

bool isEncryptedEntryType(uint32_t t) {
  return t == fourcc("encv") || t == fourcc("enca") || t == fourcc("encs") ||
         t == fourcc("encm");
}

InitSignature extractInit(const ParsedFile& pf) {
  InitSignature out;
  out.path = pf.path;
  const Box* moov = nullptr;
  for (const auto& b : pf.boxes)
    if (b.type == fourcc("moov")) moov = &b;
  if (!moov)
    throw Mp4Error(ErrorKind::BadValue, "moov", "init segment has no moov");

  for (const Box& trak : moov->children) {
    if (trak.type != fourcc("trak")) continue;
    TrackSignature sig;
    const Box* tkhd = trak.findChild(fourcc("tkhd"));
    const Box* mdia = trak.findChild(fourcc("mdia"));
    if (!tkhd || !mdia)
      throw Mp4Error(ErrorKind::IllegalNesting, "moov/trak",
                     "trak missing tkhd or mdia");
    Cursor tc(pf.data.data(), tkhd->start + tkhd->size, tkhd->payloadOffset(),
              "moov/trak/tkhd");
    FullHeader tver = readFull(tc);
    if (tver.version == 1) {
      tc.u64();
      tc.u64();
      tc.u32();
    } else if (tver.version == 0) {
      tc.u32();
      tc.u32();
      tc.u32();
    } else {
      throw Mp4Error(ErrorKind::BadValue, tc.path, "tkhd version 0 or 1 only");
    }
    sig.trackId = tc.u32();

    const Box* mdhd = mdia->findChild(fourcc("mdhd"));
    const Box* hdlr = mdia->findChild(fourcc("hdlr"));
    if (!mdhd || !hdlr)
      throw Mp4Error(ErrorKind::IllegalNesting, "moov/trak/mdia",
                     "mdia missing mdhd or hdlr");
    sig.timescale = readTimescale(pf, *mdhd);
    sig.handlerType = readHandlerType(pf, *hdlr);

    const Box* minf = mdia->findChild(fourcc("minf"));
    const Box* stbl = minf ? minf->findChild(fourcc("stbl")) : nullptr;
    const Box* stsd = stbl ? stbl->findChild(fourcc("stsd")) : nullptr;
    if (!stsd || stsd->children.empty())
      throw Mp4Error(ErrorKind::BadValue, "moov/trak/mdia/minf/stbl/stsd",
                     "no sample entry");
    const Box& entry = stsd->children.front();
    sig.sampleEntry.entryType = entry.type;
    if (isEncryptedEntryType(entry.type))
      out.encryptedBoxes.push_back(pf.path + " :: moov/trak/mdia/minf/stbl/" +
                                   typeName(entry.type));
    if (entry.findChild(fourcc("sinf")))
      out.encryptedBoxes.push_back(pf.path +
                                   " :: moov/trak/mdia/minf/stbl/sinf");
    const uint8_t* p = pf.data.data() + entry.payloadOffset();
    sig.sampleEntry.payload.assign(p, p + entry.payloadSize());
    out.tracks.push_back(std::move(sig));
  }

  const Box* mvex = moov->findChild(fourcc("mvex"));
  if (!mvex)
    throw Mp4Error(ErrorKind::BadValue, "moov/mvex",
                   "fragmented init must contain mvex");
  size_t ti = 0;
  for (const Box& trex : mvex->children) {
    if (trex.type != fourcc("trex")) continue;
    if (ti >= out.tracks.size())
      throw Mp4Error(ErrorKind::BadValue, "moov/mvex/trex",
                     "more trex boxes than traks");
    readTrex(pf, trex, out.tracks[ti]);
    ++ti;
  }
  if (ti != out.tracks.size())
    throw Mp4Error(ErrorKind::BadValue, "moov/mvex/trex",
                   "trex count does not match trak count");
  return out;
}

namespace {
std::string hex(const Bytes& b) {
  static const char* d = "0123456789abcdef";
  std::string s;
  for (uint8_t x : b) { s.push_back(d[x >> 4]); s.push_back(d[x & 0xf]); }
  return s;
}
void diffField(std::vector<FieldDiff>& out, uint32_t id, const char* field,
               const std::string& a, const std::string& b) {
  if (a != b) out.push_back({id, field, a, b});
}
std::string u(uint32_t v) { return std::to_string(v); }
} // namespace

InitCompatReport compareInits(const std::vector<InitSignature>& inits) {
  InitCompatReport rep;
  for (const auto& s : inits)
    for (const std::string& e : s.encryptedBoxes)
      rep.encryptedRejections.push_back(e);
  if (!rep.encryptedRejections.empty()) { rep.compatible = false; return rep; }
  if (inits.size() < 2) return rep;

  const InitSignature& base = inits.front();
  for (size_t i = 1; i < inits.size(); ++i) {
    const InitSignature& cur = inits[i];
    if (cur.tracks.size() != base.tracks.size()) {
      rep.compatible = false;
      rep.diffs.push_back(
          {0, "track_count", u(uint32_t(base.tracks.size())),
           u(uint32_t(cur.tracks.size()))});
      continue;
    }
    for (size_t k = 0; k < base.tracks.size(); ++k) {
      const TrackSignature& a = base.tracks[k];
      const TrackSignature& b = cur.tracks[k];
      std::vector<FieldDiff> d;
      diffField(d, a.trackId, "track_id", u(a.trackId), u(b.trackId));
      diffField(d, a.trackId, "handler_type", a.handlerType, b.handlerType);
      diffField(d, a.trackId, "timescale", u(a.timescale), u(b.timescale));
      diffField(d, a.trackId, "sample_entry_type",
                typeName(a.sampleEntry.entryType),
                typeName(b.sampleEntry.entryType));
      diffField(d, a.trackId, "sample_entry_payload",
                hex(a.sampleEntry.payload), hex(b.sampleEntry.payload));
      diffField(d, a.trackId, "trex_default_sample_duration",
                u(a.trexDefDuration), u(b.trexDefDuration));
      diffField(d, a.trackId, "trex_default_sample_size",
                u(a.trexDefSize), u(b.trexDefSize));
      diffField(d, a.trackId, "trex_default_sample_flags",
                u(a.trexDefFlags), u(b.trexDefFlags));
      diffField(d, a.trackId, "trex_sample_description_index",
                u(a.trexDescIndex), u(b.trexDescIndex));
      if (!d.empty()) { rep.compatible = false; rep.diffs.insert(
          rep.diffs.end(), d.begin(), d.end()); }
    }
  }
  return rep;
}

} // namespace mp4flow
