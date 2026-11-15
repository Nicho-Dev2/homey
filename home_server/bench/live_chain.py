#!/usr/bin/env python3
"""The whole chain in one pass, the way live TV would run it:

    decode -> cleanup network (Core ML, Neural Engine) -> AI 2x upscale (VideoToolbox, GPU)
           -> sharpen -> hardware HEVC at 1440p

The cleanup step runs in its own process and hands frames to the upscale step
through a pipe, so the two really work at the same time.

    python3 live_chain.py clip_720p.ts                        (uses the look1 cleanup network)
    python3 live_chain.py quality/rush.ts                     (test clip: also prints the score table)
    python3 live_chain.py clip_720p.ts --no-cleanup           (upscale + sharpen only, for comparison)
    python3 live_chain.py clip_720p.ts --pace --loop 5 --no-file     (heat test: 30 minutes at live-TV pace)

Without --pace the clip is processed as fast as possible, and the frames-per-second
figure is the chain's capacity: live TV needs 59.94. With --pace the clip is fed at
live speed, and a figure under 59.94 means the chain is falling behind.

Needs one Python that has everything:   (in .venv-coreml)   pip install pyobjc numpy coremltools
Output: <clip>_chain_<run>_40M_1440p.mp4 next to the clip, with the original sound.
The 3-frame cleanup network adds one frame (17 ms) of delay; picture and sound stay in step.
"""
import argparse, json, os, subprocess, sys, tempfile, time
from pathlib import Path
import numpy as np

HERE = Path(__file__).resolve().parent
W, H = 1280, 720
FRAME = W * H * 3 // 2          # one 720p frame in NV12
FPS = 60000 / 1001
# Raw pictures on a pipe carry no colour label. Newer ffmpeg then *converts* them when the output asks
# for HD colour (bt709), which shifts brightness and colour. Labelling them first prevents that.
HD_COLOUR = "setparams=colorspace=bt709:color_primaries=bt709:color_trc=bt709"


def read_exact(pipe, n):
    buf = bytearray(n)
    view, got = memoryview(buf), 0
    while got < n:
        k = pipe.readinto(view[got:])
        if not k:
            return None
        got += k
    return buf


def start_decoder(a):
    cmd = ["ffmpeg", "-hide_banner", "-loglevel", "error"]
    if a.pace:
        cmd += ["-re"]                       # read at live speed
    if a.loop:
        cmd += ["-stream_loop", str(a.loop)]
    if a.seconds:
        cmd += ["-t", f"{a.seconds:g}"]
    cmd += ["-i", str(a.clip), "-map", "0:v:0", "-vf", "fps=60000/1001:start_time=0,format=nv12", "-f", "rawvideo", "pipe:1"]
    return subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)


def windows(frames, T):
    """Lists of T frames centered on each one; the edge frame is repeated at the ends."""
    half, buf = T // 2, []
    for p in frames:
        if not buf:
            buf = [p] * (half + 1)
        else:
            buf.append(p)
        if len(buf) == T:
            yield list(buf)
            buf.pop(0)
    for _ in range(half):
        if not buf:
            break
        buf.append(buf[-1])
        if len(buf) == T:
            yield list(buf)
            buf.pop(0)


# --------------------------------------------------------------------------- #
# Stage 1 (its own process): decode + cleanup, writes cleaned NV12 frames to stdout
# --------------------------------------------------------------------------- #
def stage_cleanup(a):
    run = HERE / "cleanup" / "runs" / a.run
    if a.fake:
        T = 3
        predict = lambda x: x[0, 6 * (T // 2):6 * (T // 2) + 6]
    else:
        import coremltools as ct
        info = json.loads((run / "coreml.json").read_text())
        T = info["frames"]
        ml = ct.models.MLModel(str(run / "cleanup.mlpackage"), compute_units=ct.ComputeUnit.CPU_AND_NE)
        predict = lambda x: next(iter(ml.predict({"x": x}).values()))[0]

    dec = start_decoder(a)
    out = sys.stdout.buffer
    t_pack = t_model = t_unpack = 0.0
    n = 0
    y_out = np.empty((H, W), np.uint8)
    uv_out = np.empty((H // 2, W), np.uint8)

    def packed():
        nonlocal t_pack
        while True:
            buf = read_exact(dec.stdout, FRAME)
            if buf is None:
                return
            t = time.perf_counter()
            y = np.frombuffer(buf, np.uint8, W * H).reshape(H, W)
            uv = np.frombuffer(buf, np.uint8, offset=W * H).reshape(H // 2, W)     # U,V interleaved
            p = np.empty((1, 6, H // 2, W // 2), np.float32)
            p[0, 0], p[0, 1], p[0, 2], p[0, 3] = y[0::2, 0::2], y[0::2, 1::2], y[1::2, 0::2], y[1::2, 1::2]
            p[0, 4], p[0, 5] = uv[:, 0::2], uv[:, 1::2]
            p *= 1 / 255
            t_pack += time.perf_counter() - t
            yield p

    try:
        for win in windows(packed(), T):
            t0 = time.perf_counter()
            o = predict(np.concatenate(win, axis=1))
            t1 = time.perf_counter()
            o = (np.clip(o.astype(np.float32), 0, 1) * 255 + 0.5).astype(np.uint8)
            y_out[0::2, 0::2], y_out[0::2, 1::2], y_out[1::2, 0::2], y_out[1::2, 1::2] = o[0], o[1], o[2], o[3]
            uv_out[:, 0::2], uv_out[:, 1::2] = o[4], o[5]
            t2 = time.perf_counter()
            t_model += t1 - t0
            t_unpack += t2 - t1
            out.write(y_out.tobytes())
            out.write(uv_out.tobytes())
            n += 1
        out.flush()
    except BrokenPipeError:
        pass
    finally:
        dec.kill()
        if a.stats and n:
            Path(a.stats).write_text(json.dumps({"frames": n, "pack": 1000 * t_pack / n,
                                                 "model": 1000 * t_model / n, "unpack": 1000 * t_unpack / n}))


# --------------------------------------------------------------------------- #
# Stage 2 (main process): upscale, sharpen + encode
# --------------------------------------------------------------------------- #
def start_encoder(a, out_path, w, h):
    cmd = ["ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
           "-f", "rawvideo", "-pix_fmt", "nv12", "-s", f"{w}x{h}", "-r", "60000/1001", "-i", "pipe:0"]
    sound = not a.loop and not a.no_file
    if sound:
        limit = ["-t", f"{a.seconds:g}"] if a.seconds else []
        cmd += [*limit, "-i", str(a.clip), "-map", "0:v", "-map", "1:a?", "-c:a", "copy"]
    vf = HD_COLOUR + "," + (f"cas=strength={a.sharpness:g}," if a.sharpness > 0 else "") + "format=yuv420p"
    cmd += ["-vf", vf, "-colorspace", "bt709", "-color_primaries", "bt709", "-color_trc", "bt709",
            "-color_range", "tv", "-c:v", a.encoder, "-b:v", f"{a.bitrate:g}M"]
    if "hevc" in a.encoder:
        cmd += ["-tag:v", "hvc1"]
    cmd += ["-f", "null", "-"] if a.no_file else [str(out_path)]
    return subprocess.Popen(cmd, stdin=subprocess.PIPE)


def stage_main(a):
    sys.path.insert(0, str(HERE))
    import ai_upscale_bench as B
    if not a.fake and not a.verbose:
        B.quiet_apple_logs()
    sr = B.FakeSR() if a.fake else B.AppleSR()
    tw = B.pick_tile_width(sr, W, H)
    if not tw:
        sys.exit("This Mac did not offer a tile size for 2x upscaling of 720p.")
    up = B.Tiled2x(sr, W, H, "420v", tw)

    stats_path = child_log = child = None
    if a.no_cleanup:
        source = start_decoder(a)
        label = "nocleanup"
    else:
        stats_path = tempfile.mktemp(suffix=".json")
        child_log = tempfile.TemporaryFile()
        cmd = [sys.executable, str(Path(__file__).resolve()), str(a.clip), "--stage", "cleanup", "--run", a.run,
               "--stats", stats_path]
        for flag, on in (("--pace", a.pace), ("--fake", a.fake)):
            if on:
                cmd.append(flag)
        if a.loop:
            cmd += ["--loop", str(a.loop)]
        if a.seconds:
            cmd += ["--seconds", f"{a.seconds:g}"]
        source = child = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=child_log)
        label = a.run
    out_path = a.clip.with_name(f"{a.clip.stem}_chain_{label}_{a.bitrate:g}M_{up.out_h}p.mp4")
    enc = start_encoder(a, out_path, up.out_w, up.out_h)

    print(f"Clip: {a.clip.name}   cleanup: {'off' if a.no_cleanup else a.run}   "
          f"output: {up.out_w}x{up.out_h} at {a.bitrate:g} Mbps{'' if a.no_file else ' -> ' + out_path.name}")
    print(f"{'paced at live speed' if a.pace else 'as fast as possible'}; live TV needs {FPS:.2f} frames a second\n")
    print("minute   frames/s   upscale ms   waiting for input ms   writing output ms", flush=True)
    n = 0
    t_up = t_in = t_out = 0.0
    start = mark = time.perf_counter()
    startup = 0.0
    mark_n, m_up, m_in, m_out, minutes = 0, 0.0, 0.0, 0.0, []
    try:
        while True:
            t0 = time.perf_counter()
            buf = read_exact(source.stdout, FRAME)
            if buf is None:
                break
            t1 = time.perf_counter()
            if n == 0:                       # the wait for the first frame is start-up (loading the
                startup = t1 - start         # networks), not processing speed: keep it out of the timing
                start = mark = t0 = t1
            y = np.frombuffer(buf, np.uint8, W * H).reshape(H, W)
            uv = np.frombuffer(buf, np.uint8, offset=W * H).reshape(H // 2, W)
            out = up.process([y, uv], n)
            t2 = time.perf_counter()
            enc.stdin.write(out[0].tobytes())
            enc.stdin.write(out[1].tobytes())
            t3 = time.perf_counter()
            n += 1
            m_in += t1 - t0; m_up += t2 - t1; m_out += t3 - t2
            if t3 - mark >= 60:
                k = n - mark_n
                minutes.append(k / (t3 - mark))
                print(f"{len(minutes):5d}   {minutes[-1]:8.1f}   {1000 * m_up / k:10.1f}   "
                      f"{1000 * m_in / k:20.1f}   {1000 * m_out / k:17.1f}", flush=True)
                t_up += m_up; t_in += m_in; t_out += m_out
                mark, mark_n, m_up, m_in, m_out = t3, n, 0.0, 0.0, 0.0
    except (BrokenPipeError, KeyboardInterrupt):
        print("(stopped early)")
    t_up += m_up; t_in += m_in; t_out += m_out
    total = time.perf_counter() - start
    try:
        enc.stdin.close()
    except OSError:
        pass
    enc.wait()
    if child is None:
        source.kill()
    else:
        try:
            child.wait(timeout=20)
        except subprocess.TimeoutExpired:
            child.kill()
    up.stop()
    if not n:
        if child_log:
            child_log.seek(0)
            print(child_log.read().decode(errors="replace")[-1500:])
        sys.exit("No frames came through. See the messages above.")

    fps = n / total
    print(f"\nStart-up: {startup:.1f} s before the first frame (loading the networks)")
    print(f"{n} frames in {total:.1f} s = {fps:.1f} frames a second "
          f"({'keeps up with' if fps >= FPS - 0.3 else 'TOO SLOW for'} live TV{'' if a.pace else f'; {fps / FPS:.1f}x live speed'})")
    print("Per frame, on average:")
    if stats_path and os.path.exists(stats_path):
        s = json.loads(Path(stats_path).read_text())
        print(f"  cleanup process   {s['pack'] + s['model'] + s['unpack']:5.1f} ms  "
              f"(network {s['model']:.1f}, copying in {s['pack']:.1f}, copying out {s['unpack']:.1f})")
        os.remove(stats_path)
    print(f"  upscale           {1000 * t_up / n:5.1f} ms")
    print(f"  waiting for input {1000 * t_in / n:5.1f} ms   (time the upscaler sat idle for the step before it)")
    print(f"  writing output    {1000 * t_out / n:5.1f} ms   (sharpen + encode keeping up)")
    if len(minutes) >= 2:
        print(f"First minute {minutes[0]:.1f} frames/s, last minute {minutes[-1]:.1f} "
              f"({100 * (minutes[-1] / minutes[0] - 1):+.0f}%: watch this for heat on the Air)")
    if enc.returncode:
        print("!! The encoder reported an error; the output file may be incomplete.")
    ref = a.clip.with_name(f"{a.clip.stem}_ref.mkv")
    if not a.no_file and not a.no_score and ref.exists():
        print(flush=True)
        subprocess.run([sys.executable, str(HERE / "quality_bench.py"), "score", a.clip.stem])


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("clip", type=Path)
    ap.add_argument("--run", default="look1", help="cleanup network (a folder in cleanup/runs with cleanup.mlpackage)")
    ap.add_argument("--no-cleanup", action="store_true")
    ap.add_argument("--sharpness", type=float, default=0.4)
    ap.add_argument("--bitrate", type=float, default=40.0, help="Mbps (default 40)")
    ap.add_argument("--encoder", default="hevc_videotoolbox")
    ap.add_argument("--seconds", type=float)
    ap.add_argument("--pace", action="store_true", help="feed the clip at live-TV speed")
    ap.add_argument("--loop", type=int, default=0, help="repeat the clip this many extra times (no sound)")
    ap.add_argument("--no-file", action="store_true", help="encode but do not keep the result")
    ap.add_argument("--no-score", action="store_true")
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--stage", help=argparse.SUPPRESS)
    ap.add_argument("--stats", help=argparse.SUPPRESS)
    ap.add_argument("--fake", action="store_true", help=argparse.SUPPRESS)
    a = ap.parse_args()
    if not a.clip.exists():
        sys.exit(f"No such file: {a.clip}")
    a.clip = a.clip.resolve()
    if a.stage == "cleanup":
        stage_cleanup(a)
    else:
        if not a.no_cleanup and not a.fake and not (HERE / "cleanup" / "runs" / a.run / "cleanup.mlpackage").exists():
            sys.exit(f"cleanup/runs/{a.run}/cleanup.mlpackage not found. Run:  python3 cleanup/export_coreml.py {a.run}")
        stage_main(a)


if __name__ == "__main__":
    main()
