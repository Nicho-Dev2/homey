#!/usr/bin/env python3
"""The picture enhancer for one live stream. ota_upscaler.py runs this.

    broadcast (MPEG-TS) on stdin  ->  enhanced HEVC (MPEG-TS) on stdout
    --format 720p:  1280x720 at 59.94         ->  1440p at 59.94
    --format 1080i: 1920x1080 interlaced      ->  4K at 59.94

    stdin --+--> cleanup_stage.py: decode + cleanup network   (its own process, Neural Engine)
            |        | cleaned pictures, 59.94 a second (1080i is deinterlaced there too)
            |        v
            |    2x AI upscale, here                           (upscale.py, GPU)
            |        | pictures at twice the size
            |        v
            +--> ffmpeg: hardware HEVC encode, with the original sound  --> stdout

Light sharpening is done by ffmpeg: for 720p in the encoder step (on the 1440p picture), for
1080i in the cleanup stage (on the 1080p picture, since 4K is too many pixels to sharpen live).

Loading the networks takes several seconds, so the upscaler starts a worker ahead
of time. The worker loads everything, prints READY, then waits for a broadcast to
arrive on stdin. It handles that one stream and exits when the stream ends.

Try it by hand on a recording:
    .venv/bin/python worker.py < ../bench/clip_720p.ts > ~/Desktop/enhanced.ts
    .venv/bin/python worker.py --format 1080i < ../bench/clip_1080i.ts > ~/Desktop/enhanced.ts
"""
import argparse
import os
import queue
import subprocess
import sys
import threading
import time
from pathlib import Path
import numpy as np
from common import BITRATE, FORMATS, OUT_FPS, FAKE, SHARPNESS, read_exact, say
from upscale import Upscaler2x, quiet_apple_logs

HERE = Path(__file__).resolve().parent
CHUNK = 188 * 1024               # the broadcast is read in multiples of its 188-byte packets
CHECK_EVERY = 60                 # seconds between checks that the enhancer is keeping up with live TV


def parse_args():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--format", choices=list(FORMATS), default="720p", help="what the broadcast is (default 720p)")
    p.add_argument("--model", help="cleanup network (default: the one in models/ for this format)")
    p.add_argument("--bitrate", default=BITRATE, help=f"output video bitrate (default {BITRATE}; see common.py before raising it)")
    p.add_argument("--sharpen-with", choices=list(SHARPNESS),
                   help="1080i only, for comparing: the sharpening filter (default: the format's own, see common.py)")
    p.add_argument("--sharpness", type=float,
                   help="0 = no sharpening (default: the filter's own strength; the filters use different scales)")
    p.add_argument("--upscale", choices=("on", "off"), default="on",
                   help="off: skip the 2x AI upscale and send the cleaned pictures at their own size")
    p.add_argument("--ffmpeg", default="ffmpeg")
    return p.parse_args()


def start_encoder(cfg, width, height, start, sound_fd, sharpness):
    """ffmpeg: upscaled frames on stdin + the broadcast (for its sound) on sound_fd -> MPEG-TS on stdout.
    'start' is the broadcast-clock time of frame 0 (the first picture). With -copyts nothing
    is re-timed: the frames are placed at start + N/59.94 and the sound keeps the broadcast's
    own times. The sound may begin up to a second before the first picture, as it does on air."""
    sharpen = f"format=yuv420p,cas=strength={sharpness:g}," if sharpness > 0 else ""    # cas needs separate color planes
    # Raw frames on a pipe carry no colour label. Newer ffmpeg then *converts* them when the output asks
    # for HD colour (bt709), which darkens and tints the picture. Labelling them first prevents that.
    hd_colour = "setparams=colorspace=bt709:color_primaries=bt709:color_trc=bt709,"
    video = (["-c:v", "libx264", "-preset", "ultrafast"] if FAKE else
             ["-c:v", "hevc_videotoolbox", "-realtime", "1", "-allow_sw", "0"])
    return subprocess.Popen(
        [cfg.ffmpeg, "-hide_banner", "-loglevel", "level+error", "-nostats", "-copyts",
         "-itsoffset", f"{start:.6f}", "-f", "rawvideo", "-pix_fmt", "nv12", "-s", f"{width}x{height}", "-framerate", OUT_FPS, "-i", "pipe:0",
         "-fflags", "+genpts+discardcorrupt", "-probesize", "1000000", "-analyzeduration", "1000000",
         "-f", "mpegts", "-i", f"pipe:{sound_fd}",
         "-map", "0:v", "-map", "1:a?", "-c:a", "copy",       # every sound track, untouched
         "-vf", f"{hd_colour}{sharpen}setsar=1",
         "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709", "-color_range", "tv",
         *video, "-b:v", cfg.bitrate, "-maxrate", cfg.bitrate, "-bufsize", cfg.bitrate,
         "-g", "60", "-bf", "0",                              # same fast-start settings as the standard path
         "-f", "mpegts", "-muxdelay", "0", "-muxpreload", "0", "-flush_packets", "1", "pipe:1"],
        stdin=subprocess.PIPE, pass_fds=(sound_fd,))          # stdout: inherited, i.e. the viewer


def read_broadcast(first, *queues):
    """Put every piece of the incoming broadcast on each queue. The two users of the broadcast
    (the picture side and the sound side) each get their own queue, so a pause in one
    never holds up the other."""
    data = first
    try:
        while data:
            for q in queues:
                q.put(data)
            data = sys.stdin.buffer.read1(CHUNK)
    finally:
        for q in queues:
            q.put(None)                            # None = the broadcast has ended


def feed(q, pipe):
    """Write a queue's pieces into a pipe until the broadcast ends or the reader goes away."""
    try:
        with pipe:
            while (data := q.get()) is not None:
                pipe.write(data)
    except (BrokenPipeError, OSError):
        pass


def write_pictures(encoder, todo, free, failed):
    """Hand finished pictures to the encoder in order, while the main loop makes the next one.
    A picture's buffer goes back on `free` once it is written, so it can be filled again."""
    while (item := todo.get()) is not None:
        planes, buffer = item
        if not failed.is_set():
            try:
                for plane in planes:
                    encoder.stdin.write(plane)
            except (BrokenPipeError, OSError, ValueError):
                failed.set()                       # the viewer stopped watching; keep handing buffers back
        if buffer is not None:
            free.put(buffer)


def main():
    cfg = parse_args()
    fmt = FORMATS[cfg.format]
    cfg.model = cfg.model or str(HERE / "models" / fmt.model)
    if cfg.sharpen_with and not fmt.interlaced and cfg.sharpen_with != fmt.sharpener:
        sys.exit(f"--sharpen-with can only be chosen for 1080i; {fmt.name} always uses {fmt.sharpener}.")
    cfg.sharpen_with = cfg.sharpen_with or fmt.sharpener
    if cfg.sharpness is None:
        cfg.sharpness = SHARPNESS[cfg.sharpen_with]
    if not FAKE:
        quiet_apple_logs()

    # 1. Load everything, then say so.
    cleanup = subprocess.Popen([sys.executable, str(HERE / "cleanup_stage.py"), cfg.model, cfg.ffmpeg, fmt.name,
                                str(cfg.sharpness), cfg.sharpen_with],
                               stdin=subprocess.PIPE, stdout=subprocess.PIPE)
    upscaler = Upscaler2x(fmt) if cfg.upscale == "on" else None
    out_w, out_h = (upscaler.out_w, upscaler.out_h) if upscaler else (fmt.width, fmt.height)
    if cleanup.stdout.readline() != b"ready\n":
        sys.exit("The cleanup stage did not start (see the messages above).")
    say("READY")

    # 2. Wait for a broadcast. Nothing ever arriving means this worker was not needed.
    first = sys.stdin.buffer.read1(CHUNK)
    if not first:
        cleanup.kill()
        return
    for_picture, for_sound = queue.Queue(), queue.Queue()
    threading.Thread(target=read_broadcast, args=(first, for_picture, for_sound), daemon=True).start()
    threading.Thread(target=feed, args=(for_picture, cleanup.stdin), daemon=True).start()

    # 3. The cleanup stage says where frame 0 sits on the broadcast's clock; start the
    #    encoder with that, and give it the broadcast for its sound.
    header = cleanup.stdout.readline()
    if not header.startswith(b"start="):
        sys.exit("No picture could be decoded from this broadcast.")
    sound_read, sound_write = os.pipe()
    sharpen_here = 0 if fmt.interlaced else cfg.sharpness      # 1080i was sharpened in the cleanup stage
    encoder = start_encoder(cfg, out_w, out_h, float(header[6:]), sound_read, sharpen_here)
    os.close(sound_read)
    threading.Thread(target=feed, args=(for_sound, os.fdopen(sound_write, "wb", buffering=0)), daemon=True).start()

    # 4. Picture by picture: cleaned in, twice the size out. A second thread writes each finished
    #    picture to the encoder while the next one is made; with two output buffers, one can be
    #    written while the other is filled. The clock notes where the time goes.
    todo, free, failed = queue.Queue(maxsize=2), queue.Queue(), threading.Event()
    if upscaler:
        for _ in range(2):
            free.put(upscaler.new_output())
    writer = threading.Thread(target=write_pictures, args=(encoder, todo, free, failed), daemon=True)
    writer.start()
    y_size, n = fmt.width * fmt.height, 0
    spent = {"waiting for cleanup": 0.0, "upscaling": 0.0, "waiting for the encoder": 0.0}
    began = mark = checked = time.monotonic()
    n_checked, spent_checked = 0, dict(spent)      # the counts at the last per-minute check

    def clock(what):
        nonlocal mark
        now = time.monotonic()
        spent[what] += now - mark
        mark = now

    try:
        while not failed.is_set() and (frame := read_exact(cleanup.stdout, fmt.frame_bytes)) is not None:
            clock("waiting for cleanup")
            y = np.frombuffer(frame, np.uint8, y_size).reshape(fmt.height, fmt.width)
            uv = np.frombuffer(frame, np.uint8, offset=y_size).reshape(fmt.height // 2, fmt.width)
            buffer = free.get() if upscaler else None          # an output buffer the encoder has finished with
            clock("waiting for the encoder")
            if upscaler:
                y, uv = upscaler.process(y, uv, n, buffer)
            clock("upscaling")
            todo.put(((y, uv), buffer))            # numpy arrays go to the pipe as they are, without a copy
            clock("waiting for the encoder")
            n += 1
            if mark - checked >= CHECK_EVERY:      # a live broadcast arrives at 59.94; less means falling behind
                rate = (n - n_checked) / (mark - checked)
                if rate < 59:                      # say so, and where that minute's time went
                    shares = ", ".join(f"{what} {100 * (spent[what] - spent_checked[what]) / (mark - checked):.0f}%"
                                       for what in spent)
                    say(f"enhancer: only {rate:.1f} pictures a second over the last minute; live TV needs 59.9 "
                        f"(time: {shares})")
                checked, n_checked, spent_checked = mark, n, dict(spent)
    except (BrokenPipeError, OSError):
        pass                                       # the viewer stopped watching
    finally:
        todo.put(None)                             # let the writer finish what it holds, then stop
        writer.join()
        seconds = max(time.monotonic() - began, 0.001)
        shares = ", ".join(f"{what} {100 * t / seconds:.0f}%" for what, t in spent.items())
        say(f"[info] enhancer: {n} pictures in {seconds:.0f} s ({n / seconds:.1f} a second; live TV needs 59.9). "
            f"Time: {shares}")
        try:
            encoder.stdin.close()
        except OSError:
            pass
        encoder.wait()
        cleanup.kill()


if __name__ == "__main__":
    main()
