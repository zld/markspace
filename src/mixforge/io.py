"""WAV and JSON I/O with precise, location-aware failures."""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import numpy as np
import soundfile as sf

from .errors import AudioIOError

MAX_CHANNELS = 2


def _path_location(path: Path) -> str:
    return str(path)


def read_json(path: Path | str) -> Any:
    file_path = Path(path)
    try:
        text = file_path.read_text(encoding="utf-8")
    except OSError as exc:
        raise AudioIOError(
            f"cannot read project file: {exc.strerror or exc}",
            location=_path_location(file_path),
        ) from exc
    try:
        return json.loads(text)
    except json.JSONDecodeError as exc:
        raise AudioIOError(
            f"invalid JSON at line {exc.lineno}, column {exc.colno}: {exc.msg}",
            location=_path_location(file_path),
        ) from exc


def write_json(path: Path | str, payload: Any) -> None:
    file_path = Path(path)
    try:
        file_path.parent.mkdir(parents=True, exist_ok=True)
        text = json.dumps(payload, indent=2, ensure_ascii=False, allow_nan=False)
        file_path.write_text(text + "\n", encoding="utf-8")
    except (OSError, ValueError) as exc:
        raise AudioIOError(
            f"cannot write JSON: {exc}", location=_path_location(file_path)
        ) from exc


def _validate_audio(array: np.ndarray, location: str) -> np.ndarray:
    if array.ndim != 2:
        raise AudioIOError(
            f"expected 2-D audio, got {array.ndim}-D", location=location
        )
    if array.shape[1] not in (1, MAX_CHANNELS):
        raise AudioIOError(
            f"expected mono or stereo audio, got {array.shape[1]} channels",
            location=location,
        )
    if array.size and not np.all(np.isfinite(array)):
        raise AudioIOError("audio contains NaN or infinite samples", location=location)
    return array


def read_wav(path: Path | str) -> tuple[np.ndarray, int]:
    """Read a WAV file into ``(frames, channels)`` float64."""

    file_path = Path(path)
    if not file_path.is_file():
        raise AudioIOError("file does not exist", location=_path_location(file_path))
    try:
        data, sample_rate = sf.read(str(file_path), dtype="float64", always_2d=True)
    except (RuntimeError, OSError, TypeError) as exc:
        raise AudioIOError(
            f"cannot read WAV: {exc}", location=_path_location(file_path)
        ) from exc
    if sample_rate <= 0:
        raise AudioIOError("invalid sample rate", location=_path_location(file_path))
    return _validate_audio(np.asarray(data, dtype=np.float64), _path_location(file_path)), int(
        sample_rate
    )


def write_wav(path: Path | str, data: np.ndarray, sample_rate: int) -> None:
    """Write float32 WAV so preview samples survive byte-for-byte."""

    file_path = Path(path)
    location = _path_location(file_path)
    array = np.asarray(data, dtype=np.float64)
    _validate_audio(array, location)
    if sample_rate <= 0:
        raise AudioIOError("invalid sample rate", location=location)
    try:
        file_path.parent.mkdir(parents=True, exist_ok=True)
        sf.write(
            str(file_path),
            array.astype(np.float32),
            int(sample_rate),
            subtype="FLOAT",
        )
    except (RuntimeError, OSError, TypeError, ValueError) as exc:
        raise AudioIOError(f"cannot write WAV: {exc}", location=location) from exc


class WavWriter:
    """Incremental float32 WAV writer for the streaming path."""

    def __init__(self, path: Path | str, sample_rate: int, channels: int) -> None:
        self.path = Path(path)
        self.location = _path_location(self.path)
        if sample_rate <= 0:
            raise AudioIOError("invalid sample rate", location=self.location)
        if channels not in (1, MAX_CHANNELS):
            raise AudioIOError("expected mono or stereo output", location=self.location)
        self.sample_rate = int(sample_rate)
        self.channels = int(channels)
        self.frames_written = 0
        self._handle: sf.SoundFile | None = None
        try:
            self.path.parent.mkdir(parents=True, exist_ok=True)
            self._handle = sf.SoundFile(
                str(self.path),
                mode="w",
                samplerate=self.sample_rate,
                channels=self.channels,
                format="WAV",
                subtype="FLOAT",
            )
        except (RuntimeError, OSError, TypeError, ValueError) as exc:
            raise AudioIOError(f"cannot open WAV for writing: {exc}", location=self.location) from exc

    def write(self, data: np.ndarray) -> None:
        array = _validate_audio(np.asarray(data, dtype=np.float64), self.location)
        if self._handle is None:
            raise AudioIOError("writer is closed", location=self.location)
        if array.shape[1] != self.channels:
            raise AudioIOError(
                f"expected {self.channels} output channels, got {array.shape[1]}",
                location=self.location,
            )
        try:
            self._handle.write(array.astype(np.float32))
        except (RuntimeError, OSError, TypeError, ValueError) as exc:
            raise AudioIOError(
                f"cannot write WAV frames starting at {self.frames_written}: {exc}",
                location=self.location,
            ) from exc
        self.frames_written += int(array.shape[0])

    def close(self) -> None:
        if self._handle is not None:
            self._handle.close()
            self._handle = None

    def abort(self) -> None:
        """Close and remove a partially written file."""

        self.close()
        try:
            self.path.unlink(missing_ok=True)
        except OSError:
            pass

    def __enter__(self) -> "WavWriter":
        return self

    def __exit__(self, exc_type: object, exc: object, tb: object) -> None:
        self.close()


class WavSource:
    """Seekable source reader; each read is bounded by the requested range."""

    def __init__(self, path: Path | str, *, track_id: str) -> None:
        self.path = Path(path)
        self.track_id = track_id
        self.location = f"track[{track_id}].path"
        try:
            info = sf.info(str(self.path))
        except (RuntimeError, OSError, TypeError) as exc:
            raise AudioIOError(f"cannot open WAV: {exc}", location=self.location) from exc
        if info.samplerate <= 0:
            raise AudioIOError("invalid sample rate", location=self.location)
        if info.channels not in (1, MAX_CHANNELS):
            raise AudioIOError(
                f"expected mono or stereo audio, got {info.channels} channels",
                location=self.location,
            )
        self.frames = int(info.frames)
        self.sample_rate = int(info.samplerate)
        self.channels = int(info.channels)
        self._handle: sf.SoundFile | None = None

    def __enter__(self) -> "WavSource":
        return self

    def __exit__(self, exc_type: object, exc: object, tb: object) -> None:
        self.close()

    def close(self) -> None:
        if self._handle is not None:
            self._handle.close()
            self._handle = None

    def _open(self) -> sf.SoundFile:
        if self._handle is None:
            try:
                self._handle = sf.SoundFile(str(self.path))
            except (RuntimeError, OSError, TypeError) as exc:
                raise AudioIOError(
                    f"cannot reopen WAV: {exc}", location=self.location
                ) from exc
        return self._handle

    def read(self, start: int, count: int, *, block_start: int, block_frames: int) -> np.ndarray:
        """Read ``count`` frames at ``start``; failures name track and block."""

        if start < 0 or count < 0 or start + count > self.frames:
            raise AudioIOError(
                "requested source range is outside the file",
                location=self.location,
                context={
                    "requested_start": start,
                    "requested_frames": count,
                    "source_frames": self.frames,
                    "block_start": block_start,
                    "block_frames": block_frames,
                },
            )
        if count == 0:
            return np.empty((0, self.channels), dtype=np.float64)
        handle = self._open()
        try:
            handle.seek(start)
            data = handle.read(count, dtype="float64", always_2d=True)
        except (RuntimeError, OSError, TypeError) as exc:
            raise AudioIOError(
                f"cannot read WAV frames {start}:{start + count}: {exc}",
                location=self.location,
                context={
                    "track": self.track_id,
                    "source_start": start,
                    "source_frames": count,
                    "block_start": block_start,
                    "block_frames": block_frames,
                },
            ) from exc
        if len(data) != count:
            raise AudioIOError(
                f"short read: expected {count} frames, got {len(data)}",
                location=self.location,
                context={
                    "track": self.track_id,
                    "source_start": start,
                    "block_start": block_start,
                    "block_frames": block_frames,
                },
            )
        return _validate_audio(
            np.asarray(data, dtype=np.float64),
            f"{self.location} block_start={block_start}",
        )
