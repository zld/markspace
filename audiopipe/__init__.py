"""audiopipe: batch resampling and loudness normalization toolkit.

Modules:
    cli:        command line parsing and process orchestration
    discovery:  recursive input enumeration and path-conflict checks
    resample:   self-written polyphase windowed-sinc resampler
    loudness:   RMS / BS.1770-4 integrated LUFS / clipping statistics
    pipeline:   decode -> resample -> gain -> encode coordination
    report:     JSON report writing and verification
    fixtures:   synthetic audio material generation
"""

__version__ = "0.1.0"

#: Channel layouts supported by the pipeline (channel count -> layout name).
SUPPORTED_LAYOUTS = {
    1: ("mono", ("C",)),
    2: ("stereo", ("L", "R")),
    6: ("surround_5_1", ("L", "R", "C", "LFE", "Ls", "Rs")),
}

#: Exit codes used by the CLI.
EXIT_OK = 0
EXIT_FILE_FAILURE = 1
EXIT_USAGE = 2
EXIT_VERIFY_MISMATCH = 3
