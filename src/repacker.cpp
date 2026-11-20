#include "fmp4/repacker.hpp"

#include <cstdio>
#include <map>

namespace fmp4 {

std::vector<TrackDeltas> compute_decode_deltas(
    const std::vector<Fragment>& frags, const InitInfo& init) {
  std::map<uint32_t, TrackDeltas> by_track;
  for (size_t i = 1; i < frags.size(); ++i) {
    for (const auto& ta : frags[i - 1].trafs) {
      const Traf* tb = nullptr;
      for (const auto& t : frags[i].trafs)
        if (t.tfhd.track_id == ta.tfhd.track_id) tb = &t;
      if (!tb) continue;
      if (!ta.tfdt || !tb->tfdt)
        throw BoxError(frags[i].file, frags[i].moof_offset,
                       "missing tfdt for track " +
                           std::to_string(ta.tfhd.track_id));
      const InitTrack* track = init.find_track(ta.tfhd.track_id);
      const TrexDefaults* trex =
          (track && track->has_trex) ? &track->trex : nullptr;
      uint64_t dur = traf_duration(ta, trex, frags[i - 1].file);
      int64_t delta = static_cast<int64_t>(tb->tfdt->value) -
                      (static_cast<int64_t>(ta.tfdt->value) +
                       static_cast<int64_t>(dur));
      TrackDeltas& td = by_track[ta.tfhd.track_id];
      td.track_id = ta.tfhd.track_id;
      td.decode_delta_ticks.push_back(delta);
      td.next_seq.push_back(frags[i].sequence);
    }
  }
  std::vector<TrackDeltas> out;
  for (auto& kv : by_track) out.push_back(std::move(kv.second));
  return out;
}

int run_repack(const RepackOptions& opt) {
  (void)opt;
  std::fprintf(stderr, "ERROR: repack not yet implemented\n");
  return kExitUsage;
}

}  // namespace fmp4
