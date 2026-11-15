#!/usr/bin/env python3
"""Try a cleanup network on the 1080i test clips, before any 1080i-specific training.

    python3 apply_1080i.py look1 jockey_i rush_i          (clips made by quality_bench.py prepare --format 1080i)

A 1080i frame holds two half-pictures ("fields": the even lines and the odd lines), taken
1/60 s apart. The network has only ever seen whole 720p pictures, so there are two ways
to show it 1080i, and this runs both:

    frame   the interlaced frame as it is, as if it were one picture (30 a second)
    field   each field by itself, as a 1920x540 picture (60 a second); the two fields
            are cleaned separately and woven back together

Either way the cleaned frames are then deinterlaced with bwdif, exactly as the upscaler
does today, saved as ../quality/<clip>_cleanup_<mode>_<run>.mkv, and scored.

Runs in PyTorch (any picture size works there); speed is not the point of this test.
"""
import argparse, subprocess, sys
from pathlib import Path
import numpy as np
import torch
from cleanup_model import build, pack, unpack, pick_device
from apply_cleanup import windows

HERE = Path(__file__).resolve().parent
QDIR = HERE.parent / "quality"
W, H = 1920, 1080
FRAME = W * H * 3 // 2          # yuv420p: Y, then U, then V
WEAVE = "setfield=tff,bwdif=mode=send_field:parity=tff:deint=all"
# Raw pictures on a pipe carry no colour label. Newer ffmpeg then *converts* them when the output asks
# for HD colour (bt709), which shifts brightness and colour. Labelling them first prevents that.
HD_COLOUR = "setparams=colorspace=bt709:color_primaries=bt709:color_trc=bt709"


def planes(buf):
    a = np.frombuffer(buf, np.uint8)
    y = a[:W * H].reshape(H, W)
    u = a[W * H:W * H * 5 // 4].reshape(H // 2, W // 2)
    v = a[W * H * 5 // 4:].reshape(H // 2, W // 2)
    return y, u, v


def to_gpu(y, u, v, dev):
    """Planes of any size -> the network's layout, on the GPU."""
    yt = torch.from_numpy(np.ascontiguousarray(y)).to(dev).float().div_(255)[None, None]
    uvt = torch.from_numpy(np.stack([u, v])).to(dev).float().div_(255)[None]
    return pack(yt, uvt)


def from_gpu(x):
    oy, ouv = unpack(x)
    oy = (oy.clamp(0, 1) * 255 + 0.5).to(torch.uint8).cpu().numpy()[0, 0]
    ouv = (ouv.clamp(0, 1) * 255 + 0.5).to(torch.uint8).cpu().numpy()[0]
    return oy, ouv[0], ouv[1]


def clean_sequence(model, pictures, T, dev):
    """Clean a list of (y, u, v) pictures that follow each other in time."""
    out = []
    with torch.no_grad():
        for win in windows((to_gpu(*p, dev) for p in pictures), T):
            out.append(from_gpu(model(torch.cat(win, dim=1))))
    return out


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("run")
    ap.add_argument("clips", nargs="+")
    ap.add_argument("--modes", default="frame,field")
    a = ap.parse_args()

    ckpt = torch.load(HERE / "runs" / a.run / "model.pt", map_location="cpu")
    arch = ckpt.get("arch", "v1")
    T = ckpt["model"].get("frames", 1) if arch == "t" else 1
    dev = pick_device()
    model = build(arch, ckpt["model"]).to(dev).eval()
    model.load_state_dict(ckpt["state"])

    for name in a.clips:
        src = QDIR / f"{name}.ts"
        if not src.exists():
            print(f"skip {name}: {src.name} not found")
            continue
        raw = subprocess.run(["ffmpeg", "-v", "error", "-i", str(src), "-f", "rawvideo", "-pix_fmt", "yuv420p", "-"],
                             stdout=subprocess.PIPE).stdout
        frames = [planes(raw[i:i + FRAME]) for i in range(0, len(raw) - FRAME + 1, FRAME)]
        print(f"{name}: {len(frames)} interlaced frames", flush=True)

        for mode in a.modes.split(","):
            if mode == "frame":
                cleaned = clean_sequence(model, frames, T, dev)
            else:
                # Top fields (even lines) and bottom fields (odd lines) as two separate videos.
                tops = clean_sequence(model, [(y[0::2], u[0::2], v[0::2]) for y, u, v in frames], T, dev)
                bots = clean_sequence(model, [(y[1::2], u[1::2], v[1::2]) for y, u, v in frames], T, dev)
                cleaned = []
                for top, bot in zip(tops, bots):
                    woven = [np.empty((2 * t.shape[0], t.shape[1]), np.uint8) for t in top]
                    for full, t, b in zip(woven, top, bot):
                        full[0::2], full[1::2] = t, b
                    cleaned.append(woven)
            out = QDIR / f"{name}_cleanup_{mode}_{a.run}.mkv"
            enc = subprocess.Popen(["ffmpeg", "-v", "error", "-y", "-f", "rawvideo", "-pix_fmt", "yuv420p",
                                    "-s", f"{W}x{H}", "-r", "30000/1001", "-i", "-", "-vf", f"{HD_COLOUR},{WEAVE}",
                                    "-c:v", "libx264", "-preset", "fast", "-crf", "6", "-color_primaries", "bt709",
                                    "-color_trc", "bt709", "-colorspace", "bt709", str(out)], stdin=subprocess.PIPE)
            for y, u, v in cleaned:
                enc.stdin.write(y.tobytes() + u.tobytes() + v.tobytes())
            enc.stdin.close()
            enc.wait()
            print(f"  {mode}: {len(cleaned)} frames cleaned -> {out.name}", flush=True)
        subprocess.run([sys.executable, str(HERE.parent / "quality_bench.py"), "score", name])


if __name__ == "__main__":
    main()
