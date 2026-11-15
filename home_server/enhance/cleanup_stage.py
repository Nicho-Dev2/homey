#!/usr/bin/env python3
"""Step 1 of the enhancer, run as its own process by worker.py:

    broadcast (MPEG-TS on stdin) -> decode -> cleanup network -> cleaned frames on stdout

The cleanup network (models/look1*.mlpackage) removes compression damage. It was
trained in bench/cleanup and runs through Core ML on the Mac's Neural Engine. It
looks at three pictures at a time (the one before, the picture itself, the one after),
so its output runs one frame behind its input.

A 1080i broadcast is interlaced: each frame holds two half-pictures ("fields"), the
even lines and the odd lines, taken 1/60 s apart. The network cleans each field as a
picture of its own (measured: far better than cleaning the woven frame). The cleaned
frames then go through ffmpeg's deinterlacer, which turns every field into a full
picture. So for both formats what leaves this process is 59.94 full pictures a second.
For 1080i that same ffmpeg also does the light sharpening, here at 1080p, because
sharpening the finished 4K picture takes more processor time than a live stream leaves.

What this process writes to stdout, in order:
    "ready\n"            once the network is loaded
    "start=<seconds>\n"  when the broadcast begins: the broadcast's own clock time of picture 0
    then one cleaned NV12 picture after another

Timing: every picture in a broadcast carries a time on the broadcast's own clock.
"start" is that time for the first picture we could decode, and picture N belongs
N/59.94 seconds after it. worker.py's encoder puts the pictures back on the broadcast's
clock with that number, and the sound never leaves that clock, so the two stay in step.
"""
import re
import subprocess
import sys
import threading
import numpy as np
from common import FORMATS, FAKE, read_exact, say


class CleanupNetwork:
    """network.clean([before, picture, after]) -> cleaned picture. Pictures are in the network's
    own layout (see to_network / from_network); values 0..1."""

    def __init__(self, model_path, width, height):
        if FAKE:
            self.frames = 3
            self._predict = lambda x: x[0, 6:12]                 # test stand-in: return the middle picture
            return
        import coremltools as ct
        model = ct.models.MLModel(str(model_path), compute_units=ct.ComputeUnit.CPU_AND_NE)
        shape = [int(n) for n in model.get_spec().description.input[0].type.multiArrayType.shape]
        if shape[2:] != [height // 2, width // 2]:               # (1, 6 x frames, height/2, width/2)
            sys.exit(f"{model_path} is for {shape[3] * 2}x{shape[2] * 2} pictures, not {width}x{height}.")
        self.frames = shape[1] // 6
        self._predict = lambda x: next(iter(model.predict({"x": x}).values()))[0]
        self._predict(np.zeros(shape, np.float32))               # first run is slow: do it now

    def clean(self, window):
        return self._predict(np.concatenate(window, axis=1))


def pictures_in(y, uv, fmt):
    """The pictures the network cleans in one frame, as (brightness, color) pairs of views:
    the frame itself, or for an interlaced frame its top field and its bottom field."""
    return [(y[0::2], uv[0::2]), (y[1::2], uv[1::2])] if fmt.interlaced else [(y, uv)]


def to_network(y, uv):
    """One NV12 picture -> the network's layout: 6 planes at half size. The brightness
    plane is split into its four 2x2 positions; the two color planes are separated."""
    h, w = y.shape
    p = np.empty((1, 6, h // 2, w // 2), np.float32)
    p[0, 0], p[0, 1], p[0, 2], p[0, 3] = y[0::2, 0::2], y[0::2, 1::2], y[1::2, 0::2], y[1::2, 1::2]
    p[0, 4], p[0, 5] = uv[:, 0::2], uv[:, 1::2]                  # U,V are interleaved in NV12
    p *= 1 / 255
    return p


def from_network(planes, y, uv):
    """The reverse of to_network, written into the NV12 arrays y and uv."""
    o = (np.clip(planes.astype(np.float32), 0, 1) * 255 + 0.5).astype(np.uint8)
    y[0::2, 0::2], y[0::2, 1::2], y[1::2, 0::2], y[1::2, 1::2] = o[0], o[1], o[2], o[3]
    uv[:, 0::2], uv[:, 1::2] = o[4], o[5]


def windows(frames, size):
    """Turn a stream of frames into lists of `size` frames centered on each one.
    At the very start and end of the stream the edge frame is repeated."""
    half, held = size // 2, []
    for frame in frames:
        held = [frame] * (half + 1) if not held else held + [frame]
        if len(held) == size:
            yield held
            held = held[1:]
    for _ in range(half):
        if not held:
            break
        held = held + [held[-1]]
        if len(held) == size:
            yield held
            held = held[1:]


def keep_every_frame(ffmpeg):
    """The ffmpeg option that stops it adding or dropping frames of its own (the name changed)."""
    help_text = subprocess.run([ffmpeg, "-hide_banner", "-h", "long"], capture_output=True, text=True).stdout
    return ["-fps_mode", "passthrough"] if "fps_mode" in help_text else ["-vsync", "passthrough"]


def start_decoder(ffmpeg, fmt):
    """ffmpeg: broadcast on stdin -> raw NV12 frames at a steady rate, as broadcast
    (59.94 full frames a second for 720p, 29.97 interlaced frames for 1080i).

    -copyts keeps the broadcast's own clock (without it ffmpeg re-times the pictures, and
    gets it wrong by most of a second when a channel is joined between two full pictures).
    The 'probe' branch prints the first frame's time on that clock, for the "start" line.
    interl=1 keeps the two fields of an interlaced frame apart if the size has to change."""
    size = f"scale={fmt.width}:{fmt.height}" + (":interl=1" if fmt.interlaced else "")
    return subprocess.Popen(
        [ffmpeg, "-hide_banner", "-loglevel", "level+info", "-nostats", "-copyts",
         # Fast start, same settings as the standard path (see ota_upscaler.py for why).
         "-fflags", "+genpts+discardcorrupt", "-probesize", "1000000", "-analyzeduration", "1000000",
         "-f", "mpegts", "-i", "pipe:0",
         "-filter_complex", f"[0:v:0]fps={fmt.frames_a_second},{size},format=nv12,split[video][probe];"
                            "[probe]trim=end_frame=1,showinfo=checksum=0,nullsink",
         "-map", "[video]", *keep_every_frame(ffmpeg), "-f", "rawvideo", "pipe:1"],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE)      # stdin: inherited, i.e. the broadcast


def start_deinterlacer(ffmpeg, fmt, sharpness):
    """ffmpeg: cleaned interlaced frames on its stdin -> one full picture per field, lightly
    sharpened, written straight to this process's stdout. bwdif is the same deinterlacer the
    standard path uses. US broadcasts send the top field first; raw frames cannot say so themselves."""
    sharpen = f",format=yuv420p,cas=strength={sharpness:g}" if sharpness > 0 else ""    # cas needs separate color planes
    return subprocess.Popen(
        [ffmpeg, "-hide_banner", "-loglevel", "level+error", "-nostats",
         "-f", "rawvideo", "-pix_fmt", "nv12", "-s", f"{fmt.width}x{fmt.height}",
         "-framerate", fmt.frames_a_second, "-i", "pipe:0",
         "-vf", "setfield=tff,bwdif=mode=send_field:parity=tff:deint=all" + sharpen,
         *keep_every_frame(ffmpeg), "-f", "rawvideo", "-pix_fmt", "nv12", "pipe:1"],
        stdin=subprocess.PIPE)                               # stdout: inherited, i.e. worker.py


def watch_decoder_log(decoder, start, frame_seconds):
    """Hand ffmpeg's messages to the upscaler's log (it reads the channel's format from them),
    and catch the first frame's time. The probe prints '... n:0 pts:689010300 ...', where pts
    counts in frame lengths."""
    for raw in iter(decoder.stderr.readline, b""):
        line = raw.decode(errors="replace")
        if "Parsed_showinfo" in line:
            found = re.search(r"\bpts:\s*(-?\d+)", line)
            if found and not start["known"].is_set():
                start["seconds"] = int(found.group(1)) * frame_seconds
                start["known"].set()
            continue
        say(line)


def main():
    model_path, ffmpeg, fmt, sharpness = sys.argv[1], sys.argv[2], FORMATS[sys.argv[3]], float(sys.argv[4])
    out = sys.stdout.buffer
    y, uv = np.empty((fmt.height, fmt.width), np.uint8), np.empty((fmt.height // 2, fmt.width), np.uint8)
    cleaned = pictures_in(y, uv, fmt)              # where each cleaned picture goes: views into y and uv
    picture_height, picture_width = cleaned[0][0].shape
    network = CleanupNetwork(model_path, picture_width, picture_height)
    decoder = start_decoder(ffmpeg, fmt)           # waits quietly until the broadcast starts arriving
    deinterlacer = start_deinterlacer(ffmpeg, fmt, sharpness) if fmt.interlaced else None
    start = {"seconds": 0.0, "known": threading.Event()}
    threading.Thread(target=watch_decoder_log, args=(decoder, start, fmt.frame_seconds), daemon=True).start()
    out.write(b"ready\n")
    out.flush()

    def incoming():
        """Each decoded frame, as its pictures in the network's layout."""
        first = True
        while (frame := read_exact(decoder.stdout, fmt.frame_bytes)) is not None:
            if first:
                first = False
                start["known"].wait(timeout=2)     # printed before any frame; the wait is a formality
                out.write(f"start={start['seconds']:.6f}\n".encode())
                out.flush()                        # must be out before the first picture
            frame_y = np.frombuffer(frame, np.uint8, fmt.width * fmt.height).reshape(fmt.height, fmt.width)
            frame_uv = np.frombuffer(frame, np.uint8, offset=fmt.width * fmt.height).reshape(fmt.height // 2, fmt.width)
            yield [to_network(*picture) for picture in pictures_in(frame_y, frame_uv, fmt)]

    frames_out = deinterlacer.stdin if deinterlacer else out
    try:
        for window in windows(incoming(), network.frames):
            for i, (to_y, to_uv) in enumerate(cleaned):          # a field only ever sees fields of its own kind
                from_network(network.clean([frame[i] for frame in window]), to_y, to_uv)
            frames_out.write(y)                    # numpy arrays go to the pipe as they are, without a copy
            frames_out.write(uv)
        frames_out.flush()
    except BrokenPipeError:
        pass                                       # the viewer stopped watching
    finally:
        decoder.kill()
        if deinterlacer:
            try:
                deinterlacer.stdin.close()         # lets it finish the pictures it still holds
                deinterlacer.wait(timeout=5)
            except (OSError, subprocess.TimeoutExpired):
                deinterlacer.kill()


if __name__ == "__main__":
    main()
