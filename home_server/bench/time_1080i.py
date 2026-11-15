#!/usr/bin/env python3
"""How fast are the pieces a 1080i chain would need, on this Mac?

    source .venv-coreml/bin/activate
    python3 time_1080i.py                 (about 3 minutes; needs clip_1080i.ts and cleanup/runs/look1)

A 1080i broadcast is 29.97 interlaced frames a second; each frame holds two half-pictures
("fields") taken 1/60 s apart. The candidate chain is:

    cleanup network on the interlaced frames (30 a second, Neural Engine)
      -> bwdif deinterlacing to 1080p at 59.94 (processor, as today)
      -> optional AI 2x upscale to 4K in tiles (GPU)
      -> sharpen + hardware HEVC

This times each piece alone and says how much of every second it would use. Pieces on
different hardware (Neural Engine, GPU, processor) can overlap, but each has to stay
well under 100% for the chain to hold up through a whole game.
"""
import statistics, subprocess, sys, tempfile, time
from pathlib import Path
import numpy as np

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
sys.path.insert(0, str(HERE / "cleanup"))
CLIP = HERE / "clip_1080i.ts"
FPS = 60000 / 1001


def median_ms(fn, warm=15, runs=80):
    for _ in range(warm):
        fn()
    times = []
    for _ in range(runs):
        t = time.perf_counter()
        fn()
        times.append(1000 * (time.perf_counter() - t))
    return statistics.median(times)


def time_cleanup():
    """The look1 network (speed does not depend on what it was trained on), exported for a
    1920x1080 frame, and for one 1920x540 field."""
    import torch, coremltools as ct
    from cleanup_model import build
    ckpt = torch.load(HERE / "cleanup" / "runs" / "look1" / "model.pt", map_location="cpu")
    model = build(ckpt.get("arch", "v1"), ckpt["model"]).eval()
    model.load_state_dict(ckpt["state"])
    frames = ckpt["model"].get("frames", 1)
    out = {}
    for label, (h, w) in (("1920x1080 frame", (540, 960)), ("1920x540 field", (270, 960)), ("1280x720 (for reference)", (360, 640))):
        shape = (1, 6 * frames, h, w)
        traced = torch.jit.trace(model, torch.rand(*shape))
        ml = ct.convert(traced, inputs=[ct.TensorType(name="x", shape=shape)], convert_to="mlprogram",
                        compute_precision=ct.precision.FLOAT16, minimum_deployment_target=ct.target.macOS13)
        path = str(Path(tempfile.mkdtemp()) / "net.mlpackage")
        ml.save(path)
        net = ct.models.MLModel(path, compute_units=ct.ComputeUnit.CPU_AND_NE)
        x = np.random.rand(*shape).astype(np.float32)
        out[label] = median_ms(lambda: net.predict({"x": x}), warm=8, runs=50)
    return out


def time_upscale():
    """Apple's 2x upscaler on a 1920x1080 frame, as tiles. Two layouts:
    4 tiles of 960x540 (no overlap: fastest, but the joins could show), and
    6 tiles of 704x576 (overlapping, so joins can be cross-faded like on 720p)."""
    import ai_upscale_bench as B
    B.quiet_apple_logs()
    sr = B.AppleSR()
    y = np.random.randint(0, 255, (1080, 1920), np.uint8)
    uv = np.random.randint(0, 255, (540, 1920), np.uint8)
    layouts = {"4 tiles of 960x540": (960, 540, (0, 960), (0, 540)),
               "6 tiles of 704x576": (704, 576, (0, 608, 1216), (0, 504))}
    out = {}
    for label, (tw, th, xs, ys) in layouts.items():
        factors = sr.scale_factors(tw, th)
        if not (isinstance(factors, list) and any(abs(f - 2.0) < 0.01 for f in factors)):
            out[label] = None
            continue
        tiles = [(sr.session(tw, th, 2.0, "420v"), x, yy) for yy in ys for x in xs]
        dest = [np.zeros((2 * th, 2 * tw), np.uint8), np.zeros((th, 2 * tw), np.uint8)]
        n = [0]

        def one_frame():
            for sess, x, yy in tiles:
                sess.put([y[yy:yy + th, x:x + tw], uv[yy // 2:(yy + th) // 2, x:x + tw]])
                sess.run(n[0])
                sess.get(dest)
            n[0] += 1
        out[label] = median_ms(one_frame, warm=20, runs=120)
        for sess, _, _ in tiles:
            sess.stop()
    return out


def ffmpeg_fps(args, frames):
    t = time.perf_counter()
    r = subprocess.run(["ffmpeg", "-hide_banner", "-loglevel", "error", "-y", *args, "-f", "null", "-"],
                       stderr=subprocess.PIPE, text=True)
    took = time.perf_counter() - t
    return None if r.returncode else frames / took


def main():
    if not CLIP.exists():
        sys.exit(f"{CLIP.name} not found: record one with  python3 record_clips.py --only 1080i --seconds 120")
    secs = 30
    print("Timing each piece (about 3 minutes) ...\n")

    weave = ffmpeg_fps(["-t", str(secs), "-i", str(CLIP), "-map", "0:v:0",
                        "-vf", "bwdif=mode=send_field:parity=auto:deint=interlaced"], secs * FPS)
    src = lambda size: ["-f", "lavfi", "-i", f"testsrc2=size={size}:rate=60000/1001", "-t", "10", "-pix_fmt", "nv12"]
    enc = {name: ffmpeg_fps([*src(size), "-c:v", "hevc_videotoolbox", "-realtime", "1", "-b:v", rate], 10 * FPS)
           for name, size, rate in (("1080p at 20 Mbps", "1920x1080", "20M"), ("4K at 50 Mbps", "3840x2160", "50M"))}
    gen = {size: ffmpeg_fps(src(size), 10 * FPS) for size in ("1920x1080", "3840x2160")}
    cleanup = time_cleanup()
    upscale = time_upscale()

    def share(ms, per_second):
        return f"{ms:6.1f} ms each = {ms * per_second / 10:4.0f}% of every second"

    print("CLEANUP NETWORK (Neural Engine), look1 size")
    for label, ms in cleanup.items():
        rate = 59.94 if "field" in label or "720" in label else 29.97
        print(f"  {label:26} {share(ms, rate)}  ({rate:.0f} a second)")
    print("\nDEINTERLACING, bwdif on the processor (as today)")
    print(f"  1080i -> 1080p59.94         {weave:6.0f} frames a second  (needs 60)" if weave else "  failed")
    print("\nAI 2x UPSCALE 1080p -> 4K (GPU), copying included, blending of joins not included")
    for label, ms in upscale.items():
        print(f"  {label:26} {share(ms, 59.94) if ms else 'not offered by this Mac'}")
    print("\nHARDWARE HEVC ENCODE (includes making a test picture, so the true figure is higher)")
    for name, fps in enc.items():
        print(f"  {name:26} {fps:6.0f} frames a second  (needs 60)" if fps else f"  {name}: failed")
    print(f"  (making the test picture alone: 1080p {gen['1920x1080']:.0f}, 4K {gen['3840x2160']:.0f} frames a second)")
    print("\nFor comparison, the 720p chain that runs live today: cleanup about 8 ms x 60 = 48%, "
          "upscale about 3.5 ms x 60 = 21%.")


if __name__ == "__main__":
    main()
