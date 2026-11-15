#!/usr/bin/env python3
"""Step 3: run a trained cleanup network over the test clips and score the result.

    python3 apply_cleanup.py v2 nfl_a rush        (clips prepared by quality_bench.py)
    python3 apply_cleanup.py v2 --all

For each clip it reads ../quality/<clip>.ts (the simulated broadcast), cleans every
frame, saves ../quality/<clip>_cleanup_<run>.mkv, then runs quality_bench.py score.
Works for v1 (one frame) and v2 (neighboring frames) models.

A real recording works too (720p only; no score, since there is no clean original):
    python3 apply_cleanup.py v2 ../clip_720p.ts --seconds 30      -> ../clip_720p_cleanup_v2.mkv
The speed shown is PyTorch on the Mac's GPU, including copying frames in and out:
a rough guide only. speed_cleanup.py measures the network itself, also in Core ML.
"""
import argparse, subprocess, sys, time
from pathlib import Path
import numpy as np
import torch
from cleanup_model import build, pack, unpack, pick_device

HERE = Path(__file__).resolve().parent
QDIR = HERE.parent / "quality"
W, H = 1280, 720
FRAME = W * H * 3 // 2
# Raw pictures on a pipe carry no colour label. Newer ffmpeg then *converts* them when the output asks
# for HD colour (bt709), which shifts brightness and colour. Labelling them first prevents that.
HD_COLOUR = "setparams=colorspace=bt709:color_primaries=bt709:color_trc=bt709"


class TorchEngine:
    """PyTorch on the Mac's GPU (how training runs it)."""
    name = "PyTorch"

    def __init__(self, model, dev):
        self.model, self.dev = model, dev

    def pack(self, buf):                         # raw yuv420p bytes -> (1,6,360,640) on the GPU
        arr = np.frombuffer(buf, np.uint8)
        y = torch.from_numpy(arr[:W * H].reshape(1, 1, H, W).copy()).to(self.dev).float() / 255
        uv = torch.from_numpy(arr[W * H:].reshape(1, 2, H // 2, W // 2).copy()).to(self.dev).float() / 255
        return pack(y, uv)

    def clean(self, win):
        with torch.no_grad():
            oy, ouv = unpack(self.model(torch.cat(win, dim=1)))
            oy = (oy.clamp(0, 1) * 255 + 0.5).to(torch.uint8).cpu().numpy()
            ouv = (ouv.clamp(0, 1) * 255 + 0.5).to(torch.uint8).cpu().numpy()
        return oy.tobytes(), ouv.tobytes()


class CoreMLEngine:
    """The exported Core ML file, as the live version would run it. No PyTorch involved:
    frames are packed and unpacked with numpy, in the same layout cleanup_model.pack uses."""

    def __init__(self, path, unit):
        import coremltools as ct
        if not Path(path).exists():
            sys.exit(f"{path} not found. Run:  python3 export_coreml.py <run>")
        units = {"ne": ct.ComputeUnit.CPU_AND_NE, "gpu": ct.ComputeUnit.CPU_AND_GPU, "all": ct.ComputeUnit.ALL}
        self.ml = ct.models.MLModel(str(path), compute_units=units[unit])
        self.name = "Core ML (" + {"ne": "Neural Engine", "gpu": "GPU", "all": "Apple's choice"}[unit] + ")"

    def pack(self, buf):                         # raw yuv420p bytes -> (1,6,360,640) float32
        arr = np.frombuffer(buf, np.uint8)
        y = arr[:W * H].reshape(H, W)
        p = np.empty((1, 6, H // 2, W // 2), np.float32)
        p[0, 0], p[0, 1], p[0, 2], p[0, 3] = y[0::2, 0::2], y[0::2, 1::2], y[1::2, 0::2], y[1::2, 1::2]
        p[0, 4:] = arr[W * H:].reshape(2, H // 2, W // 2)
        p *= 1 / 255
        return p

    def clean(self, win):
        o = next(iter(self.ml.predict({"x": np.concatenate(win, axis=1)}).values()))[0]
        o = (np.clip(o.astype(np.float32), 0, 1) * 255 + 0.5).astype(np.uint8)
        y = np.empty((H, W), np.uint8)
        y[0::2, 0::2], y[0::2, 1::2], y[1::2, 0::2], y[1::2, 1::2] = o[0], o[1], o[2], o[3]
        return y.tobytes(), o[4:6].tobytes()


def windows(frames, T):
    """Turn a stream of frames into lists of T frames centered on each one.
    At the start and end of the clip the edge frame is repeated."""
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


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("run")
    ap.add_argument("clips", nargs="*")
    ap.add_argument("--all", action="store_true")
    ap.add_argument("--no-score", action="store_true")
    ap.add_argument("--seconds", type=float, help="only the first N seconds of each clip")
    ap.add_argument("--coreml", nargs="?", const="ne", choices=["ne", "gpu", "all"],
                    help="run the exported Core ML file (export_coreml.py) instead of PyTorch; "
                         "ne = Neural Engine (default), gpu, or all = let Apple decide")
    a = ap.parse_args()

    ckpt = torch.load(HERE / "runs" / a.run / "model.pt", map_location="cpu")
    arch = ckpt.get("arch", "v1")
    T = ckpt["model"].get("frames", 1) if arch == "t" else 1
    half = T // 2
    label = a.run + ("_coreml" if a.coreml else "")
    if a.coreml:
        engine = CoreMLEngine(HERE / "runs" / a.run / "cleanup.mlpackage", a.coreml)
    else:
        dev = pick_device()
        model = build(arch, ckpt["model"]).to(dev).eval()
        model.load_state_dict(ckpt["state"])
        engine = TorchEngine(model, dev)
    clips = a.clips
    if a.all:
        clips = sorted(p.stem for p in QDIR.glob("*.ts")      # 720p test clips only ("_i" = 1080i)
                       if not p.stem.endswith(("_original_720p", "_i")))
    if not clips:
        sys.exit("Name some clips or use --all.")

    for name in clips:
        real = Path(name).expanduser()               # a file path = a real recording, not a test clip
        if real.suffix and real.exists():
            src, out, name = real, real.with_name(f"{real.stem}_cleanup_{label}.mkv"), real.stem
            score = False
        else:
            src, out, score = QDIR / f"{name}.ts", QDIR / f"{name}_cleanup_{label}.mkv", not a.no_score
        if not src.exists():
            print(f"skip {name}: {src} not found")
            continue
        limit = ["-t", f"{a.seconds:g}"] if a.seconds else []
        steady = [] if score or a.no_score and src.parent == QDIR else ["-vf", "fps=60000/1001:start_time=0"]     # frames start where the file starts, so sound stays in step
        dec = subprocess.Popen(["ffmpeg", "-v", "error", "-i", str(src), *limit, *steady,
                                "-f", "rawvideo", "-pix_fmt", "yuv420p", "-"], stdout=subprocess.PIPE)
        enc = subprocess.Popen(["ffmpeg", "-v", "error", "-y", "-f", "rawvideo", "-pix_fmt", "yuv420p",
                                "-s", f"{W}x{H}", "-r", "60000/1001", "-i", "-",
                                *limit, "-i", str(src), "-map", "0:v", "-map", "1:a?", "-c:a", "copy",   # keep the sound
                                "-vf", HD_COLOUR, "-c:v", "libx264",
                                "-preset", "fast", "-crf", "6", "-color_primaries", "bt709", "-color_trc", "bt709",
                                "-colorspace", "bt709", str(out)], stdin=subprocess.PIPE)
        n, spent = 0, 0.0

        def packed_frames():
            nonlocal spent
            while True:
                buf = dec.stdout.read(FRAME)
                if len(buf) < FRAME:
                    return
                t = time.perf_counter()
                p = engine.pack(buf)
                if n >= 10:                          # skip warm-up frames in the timing
                    spent += time.perf_counter() - t
                yield p

        for win in windows(packed_frames(), T):
            t = time.perf_counter()
            y_bytes, uv_bytes = engine.clean(win)
            if n >= 10:
                spent += time.perf_counter() - t
            enc.stdin.write(y_bytes)
            enc.stdin.write(uv_bytes)
            n += 1
        dec.stdout.close()
        dec.wait()
        enc.stdin.close()
        enc.wait()
        ms = 1000 * spent / max(n - 10, 1)
        print(f"{name}: {n} frames cleaned, {ms:.1f} ms per frame in {engine.name} -> {out.name}", flush=True)
        if score:
            subprocess.run([sys.executable, str(HERE.parent / "quality_bench.py"), "score", name])


if __name__ == "__main__":
    main()
