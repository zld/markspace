#ifndef TRANSITION_LAB_TIMELINE_HPP
#define TRANSITION_LAB_TIMELINE_HPP

#include <cstdint>
#include <string>
#include <vector>

#include "frame_buffer.hpp"
#include "transition.hpp"

namespace tl {

// Positive rational playback rate, kept normalized (gcd-reduced).
struct Rate {
    std::int64_t num = 1;
    std::int64_t den = 1;
};

// One speed node: at output frame `output_frame` the rate becomes `rate`.
struct SpeedNode {
    int output_frame = 0;
    Rate rate;
};

// Parse a "frame=num/den" token (e.g. "24=2/1"). Returns false on error.
bool parse_rate_spec(const std::string& token, SpeedNode* node,
                     std::string* error);

// Build and validate a speed map. Checks: a node at frame 0 exists,
// frames are strictly increasing within [0, max_output], rates lie in
// [1/8, 8], and cumulative source positions never overflow int64.
class SpeedMap {
public:
    bool build(std::vector<SpeedNode> nodes, int max_output,
               std::string* error);

    // Exact source position at output frame f, computed by integrating
    // the piecewise-constant rate over output time.
    std::int64_t position_num(int frame) const;
    std::int64_t position_den(int frame) const;

    // Nearest integer source frame index for the given output frame.
    int source_frame(int output_frame) const;

    const std::vector<SpeedNode>& nodes() const { return nodes_; }

private:
    std::vector<SpeedNode> nodes_;
    std::vector<std::int64_t> pos_num_;
    std::vector<std::int64_t> pos_den_;
    int max_output_ = 0;
};

// Timeline binds two source clips with a transition spanning their
// overlap, and maps output frames to source frames through a SpeedMap.
class Timeline {
public:
    bool build(int width, int height, int output_frames,
               int transition_start, int transition_length,
               TransitionType type, const TransitionParams& params,
               const SpeedMap& speed, std::uint64_t seed,
               std::string* error);

    // Render one output frame using the same transition kernel as the
    // standalone kernel path.
    bool render_frame(int output_frame, FrameBuffer* out,
                      std::string* error) const;

    int output_frames() const { return output_frames_; }
    int source_frames() const { return source_frames_; }
    TransitionType type() const { return type_; }

private:
    FrameBuffer source_frame_buffer(int clip, int index) const;

    int width_ = 0;
    int height_ = 0;
    int output_frames_ = 0;
    int transition_start_ = 0;
    int transition_length_ = 0;
    int source_frames_ = 0;
    TransitionType type_ = TransitionType::Dissolve;
    TransitionParams params_;
    SpeedMap speed_;
    std::uint64_t seed_ = 0;
};

}  // namespace tl

#endif
