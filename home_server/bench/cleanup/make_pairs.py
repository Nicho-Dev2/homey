#!/usr/bin/env python3
"""Step 1: build training data. No AI here, only ffmpeg and numpy.

For every source in config.json it cuts short chunks, shrinks them to clean 720p,
damages a copy like a TV station would (MPEG-2, bitrates from the config), then
saves small matching squares ("patches") from the damaged and the clean frames.

    python3 make_pairs.py                 (uses config.json, writes data/)
    python3 make_pairs.py --config config_v2.json --out data_v2     (with neighboring frames)

A source in the config is either one "file", or a "folder": every video inside it (and its
subfolders) is used, so new clips only need to be dropped into that folder. Folder options:
"max_files" (random pick, same every run), "chunks" per file or "chunk_every" N seconds,
"min_fps", and per-source "chunk_frames" / "frame_step" / "patches_per_frame".
Clips that are not 16:9 are center-cropped; clips smaller than 1280x720 are skipped.

"format" in the config: "720p" (default) or "1080i". For 1080i the clean footage is woven into
interlaced frames, damaged at CBS-like bitrates, and patches are cut from single fields.

"frames" in the config: 1 = each damaged patch alone; 3 = the damaged patch plus the same
spot in the frame before and the frame after (what the v2 network looks at).

Same config + same source files = same data (the random choices use the seed).
"""
import argparse, json, random, shutil, subprocess, sys, tempfile, time
from pathlib import Path
import numpy as np

HERE = Path(__file__).resolve().parent
# The two kinds of broadcast. "1080i" patches are cut from single fields (every other line),
# because that is how the network is shown 1080i (see apply_1080i.py: fields beat whole frames).
FORMATS = {
    "720p":  {"w": 1280, "h": 720, "interlaced": False, "rate": "60000/1001",
              "mpeg2": ["-maxrate", "12000k", "-bufsize", "7000k"]},
    "1080i": {"w": 1920, "h": 1080, "interlaced": True, "rate": "30000/1001",
              "mpeg2": ["-vf", "setfield=tff", "-flags", "+ildct+ilme", "-maxrate", "15000k", "-bufsize", "9000k"]},
}
RETIME = "settb=1/60000,setpts=N*1001"


def run(cmd):
    r = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if r.returncode:
        raise RuntimeError(r.stderr[-600:])
    return r.stdout


def probe(path):
    """fps, duration in seconds, width, height"""
    s = json.loads(run(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
                        "stream=avg_frame_rate,width,height:format=duration", "-of", "json", str(path)]))
    v = s["streams"][0]
    a, b = v["avg_frame_rate"].split("/")
    return float(a) / float(b), float(s["format"]["duration"]), v["width"], v["height"]


def frames(path, fmt):
    """Yield (y, u, v) uint8 arrays for each frame of a file."""
    w, h = fmt["w"], fmt["h"]
    size = w * h * 3 // 2
    p = subprocess.Popen(["ffmpeg", "-v", "error", "-i", str(path), "-f", "rawvideo",
                          "-pix_fmt", "yuv420p", "-"], stdout=subprocess.PIPE)
    try:
        while True:
            buf = p.stdout.read(size)
            if len(buf) < size:
                break
            a = np.frombuffer(buf, np.uint8)
            yield (a[:w * h].reshape(h, w), a[w * h:w * h * 5 // 4].reshape(h // 2, w // 2),
                   a[w * h * 5 // 4:].reshape(h // 2, w // 2))
    finally:
        p.stdout.close()
        p.wait()


def windows(clean_path, dmg_path, T, fmt):
    """Yield (index, clean frame, [T damaged frames centered on it]). Ends repeat the edge frame."""
    half, buf = T // 2, []
    def emit(i):
        return i, buf[i][0], [buf[min(max(j, 0), len(buf) - 1)][1] for j in range(i - half, i + half + 1)]
    done = 0
    for pair in zip(frames(clean_path, fmt), frames(dmg_path, fmt)):
        buf.append(pair)
        while done + half < len(buf):          # the frame "done" now has its later neighbors
            yield emit(done)
            done += 1
            if done > half:                    # frames far behind are no longer needed
                buf[done - half - 1] = None
    while done < len(buf):
        yield emit(done)
        done += 1


def make_chunk(src, start, n_frames, fps, mbps, tmp, fmt):
    """A clean chunk (lossless) and its MPEG-2 damaged copy, in the broadcast's own shape.
    For 1080i, 59.94 pictures a second are woven into 29.97 interlaced frames first."""
    clean, dmg = tmp / "clean.mkv", tmp / "dmg.ts"
    w, h = fmt["w"], fmt["h"]
    pick = "select=not(mod(n\\,2))," if fps > 90 else ""
    weave = "interlace=scan=tff," if fmt["interlaced"] else ""
    run(["ffmpeg", "-v", "error", "-y", "-ss", f"{start:.3f}", "-i", str(src), "-an",
         "-frames:v", str(n_frames),
         "-vf", f"{pick}{RETIME},scale={w}:{h}:force_original_aspect_ratio=increase:flags=lanczos,"
                f"crop={w}:{h},{weave}format=yuv420p", "-r", fmt["rate"],
         "-c:v", "libx264", "-preset", "ultrafast", "-qp", "0", str(clean)])
    base = ["ffmpeg", "-v", "error", "-y", "-i", str(clean), "-an", "-c:v", "mpeg2video",
            "-b:v", f"{int(mbps * 1000)}k", *fmt["mpeg2"],
            "-g", "60", "-bf", "3", "-mbd", "rd", "-trellis", "2", "-passlogfile", str(tmp / "p")]
    try:                       # two passes hit the bitrate best ...
        run(base + ["-pass", "1", "-f", "null", "-"])
        run(base + ["-pass", "2", "-f", "mpegts", str(dmg)])
    except RuntimeError:       # ... but can refuse very busy pictures; one pass always works
        run(base + ["-f", "mpegts", str(dmg)])
    return clean, dmg


def chunk_starts(duration, fps, n_frames, count, exclude, rng, pictures_per_frame=1):
    """Evenly spread start times (seconds of the source), skipping the test-only ranges."""
    per_frame = 1 / fps if fps <= 90 else 2 / fps
    length = n_frames * per_frame * pictures_per_frame      # an interlaced frame uses two pictures
    last = duration - length - 0.5
    if last <= 0:
        return []
    slots = [last * (i + 0.5) / count for i in range(count)] if count > 1 else [0.0]
    out = []
    for s in slots:
        s += rng.uniform(-0.3, 0.3) * (last / max(count, 1))
        s = min(max(s, 0.0), last)
        if any(s < b and s + length > a for a, b in exclude):
            continue
        out.append(s)
    return out


VIDEO = {".mp4", ".mkv", ".mov", ".m4v", ".webm", ".y4m", ".ts", ".mxf"}


def build_plan(cfg, base, rng, fmt):
    """One entry per chunk: (file, start second, fps, Mbps, frames, frame step, patches per frame)."""
    plan = []
    for s in cfg["sources"]:
        nfr = s.get("chunk_frames", cfg["chunk_frames"])
        step = s.get("frame_step", cfg["frame_step"])
        ppf = s.get("patches_per_frame", cfg["patches_per_frame"])
        if "folder" in s:                         # every video in a folder (and its subfolders)
            folder = (base / s["folder"]).resolve()
            files = sorted(p for p in folder.rglob("*") if p.suffix.lower() in VIDEO and not p.name.startswith("."))
            if not files:
                print(f"skip (no videos yet): {s['folder']}")
                continue
            if s.get("max_files") and len(files) > s["max_files"]:
                files = sorted(rng.sample(files, s["max_files"]))
            print(f"{s['folder']}: {len(files)} files")
        else:
            files = [(base / s["file"]).resolve()]
            if not files[0].exists():
                print(f"skip (not found): {s['file']}")
                continue
        for src in files:
            try:
                fps, dur, w, h = probe(src)
            except Exception:
                print(f"  skip (cannot read): {src.name}")
                continue
            if h < s.get("min_height", fmt["h"]) or w < fmt["w"] or fps < s.get("min_fps", 0):
                print(f"  skip ({w}x{h} {fps:.0f} fps): {src.name}")
                continue
            count = s.get("chunks", 1)
            if s.get("chunk_every"):              # one chunk per this many seconds of the file
                count = max(1, int(dur / s["chunk_every"]))
            for st in chunk_starts(dur, fps, nfr, count, s.get("exclude_seconds", []), rng,
                                   2 if fmt["interlaced"] else 1):
                plan.append((src, st, fps, rng.choice(cfg["bitrates_mbps"]), nfr, step, ppf))
    return plan


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--config", default=str(HERE / "config.json"))
    ap.add_argument("--out", default=str(HERE / "data"))
    a = ap.parse_args()
    cfg = json.loads(Path(a.config).read_text())
    rng = random.Random(cfg["seed"])
    P, T = cfg["patch"], cfg.get("frames", 1)
    out = Path(a.out)
    out.mkdir(parents=True, exist_ok=True)

    fmt = FORMATS[cfg.get("format", "720p")]
    fields = (0, 1) if fmt["interlaced"] else (None,)       # 1080i: top field, then bottom field
    plan = build_plan(cfg, Path(a.config).resolve().parent, rng, fmt)
    if not plan:
        sys.exit("No source files found. Check the paths in the config.")

    cap = sum(len(range(0, nfr, step)) * ppf * len(fields) for *_, nfr, step, ppf in plan)
    q = P // 2
    tdim = (T,) if T > 1 else ()
    names = {"dmg_y": (cap, *tdim, P, P), "dmg_uv": (cap, *tdim, 2, q, q),
             "clean_y": (cap, P, P), "clean_uv": (cap, 2, q, q)}
    gb = sum(int(np.prod(s)) for s in names.values()) / 1e9
    free = shutil.disk_usage(out).free / 1e9
    print(f"{len(plan)} chunks -> up to {cap} patches of {P}x{P} ({gb:.1f} GB) in {out}")
    if gb > free - 5:
        sys.exit(f"Not enough disk space: needs {gb:.1f} GB, {free:.1f} GB free. "
                 f"Lower max_files or patches_per_frame in the config.")
    arr = {k: np.lib.format.open_memmap(out / f"{k}.npy", "w+", np.uint8, shp) for k, shp in names.items()}

    n, log, t0 = 0, [], time.time()
    for ci, (src, st, fps, mbps, nfr, step, ppf) in enumerate(plan):
        tmp = Path(tempfile.mkdtemp())
        try:
            clean, dmg = make_chunk(src, st, nfr, fps, mbps, tmp, fmt)
            n0, sq, got = n, 0.0, 0
            for fi, c_frame, d_frames in windows(clean, dmg, T, fmt):
                got += 1
                if fi % step:
                    continue
                for field in fields:
                    # One picture to cut patches from: the whole frame, or one field of it
                    # (with the same field of the neighboring frames).
                    cut = (lambda planes: planes) if field is None else (lambda planes: [x[field::2] for x in planes])
                    c, ds = cut(c_frame), [cut(d) for d in d_frames]
                    d = ds[T // 2]                                     # the damaged picture itself
                    ph, pw = c[0].shape
                    rows = 16 if field is None else 8                  # MPEG-2 block rows, in this picture's lines
                    for _ in range(ppf):
                        y0 = rng.randrange(0, (ph - P) // rows + 1) * rows
                        x0 = rng.randrange(0, (pw - P) // 16 + 1) * 16
                        ys, xs = slice(y0, y0 + P), slice(x0, x0 + P)
                        cs, cx = slice(y0 // 2, y0 // 2 + q), slice(x0 // 2, x0 // 2 + q)
                        arr["clean_y"][n] = c[0][ys, xs]
                        arr["clean_uv"][n, 0], arr["clean_uv"][n, 1] = c[1][cs, cx], c[2][cs, cx]
                        if T > 1:
                            for t, f in enumerate(ds):
                                arr["dmg_y"][n, t] = f[0][ys, xs]
                                arr["dmg_uv"][n, t, 0], arr["dmg_uv"][n, t, 1] = f[1][cs, cx], f[2][cs, cx]
                        else:
                            arr["dmg_y"][n] = d[0][ys, xs]
                            arr["dmg_uv"][n, 0], arr["dmg_uv"][n, 1] = d[1][cs, cx], d[2][cs, cx]
                        sq += float(np.mean((c[0][ys, xs].astype(np.float32) - d[0][ys, xs]) ** 2))
                        n += 1
            if got != nfr:     # frame counts must match or the pairs would not line up
                print(f"  chunk {ci + 1}: got {got} frames, wanted {nfr}; dropped ({src.name})")
                n = n0
                continue
            psnr = 10 * np.log10(255 ** 2 / max(sq / (n - n0), 1e-9))
            log.append({"source": src.name, "start": round(st, 2), "mbps": mbps, "patches": n - n0,
                        "psnr_damaged": round(float(psnr), 2)})
            left = (time.time() - t0) / (ci + 1) * (len(plan) - ci - 1) / 60
            print(f"  chunk {ci + 1}/{len(plan)}: {src.name[:40]} at {st:6.1f}s, {mbps} Mbps, "
                  f"damaged vs clean {psnr:.1f} dB, about {left:.0f} min left", flush=True)
        except RuntimeError as e:
            print(f"  chunk {ci + 1}: ffmpeg failed, skipped ({src.name})\n{e}")
        finally:
            shutil.rmtree(tmp, ignore_errors=True)

    for m in arr.values():
        m.flush()
    (out / "meta.json").write_text(json.dumps({"patches": n, "patch": P, "frames": T, "format": cfg.get("format", "720p"), "config": cfg, "chunks": log}, indent=1))
    print(f"\n{n} patches saved in {out}.")


if __name__ == "__main__":
    main()
