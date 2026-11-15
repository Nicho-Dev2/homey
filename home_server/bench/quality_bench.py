#!/usr/bin/env python3
"""Picture-quality scoring for the upscaler, using clips where we HAVE the original.

Idea: take a clean clip, damage it the way a TV station does (shrink to 720p,
MPEG-2 at your FOX station's bitrate), run the upscaler on the damaged copy,
then measure how close each result gets back to the clean original.

    python3 quality_bench.py prepare sources/downloads/nfl_highlights.mp4 --start 60 --seconds 20 --name nfl_a
    python3 quality_bench.py run nfl_a -- --modes ai2x,tnr2x --pipeline     (Mac only: runs ai_upscale_bench.py, then scores)
    python3 quality_bench.py score nfl_a                                    (scores whatever files exist)
    python3 quality_bench.py score nfl_a --try "spp=spp=quality=2" --try "soft=hqdn3d=1.5:1:2:2"

1080i channels (CBS, NBC):
    python3 quality_bench.py prepare sources/uvg_jockey_2160p60.mkv --seconds 5 --name jockey_i --format 1080i
    python3 quality_bench.py score jockey_i --try "spp=spp=quality=2,bwdif=mode=send_field:parity=tff:deint=all"
  Here "floor" is what the upscaler does today (bwdif deinterlacing) and "ceiling" is a 1080i
  picture with no compression damage. A --try chain must do its own deinterlacing.

Files go in quality/:
    nfl_a_ref.mkv          the clean original (what we compare against)
    nfl_a.ts               the simulated broadcast (720p MPEG-2), input for ai_upscale_bench.py
    nfl_a_<mode>_4k.mp4    written by ai_upscale_bench.py
    nfl_a_<anything>.mkv   any other processed version (e.g. from cleanup/apply_cleanup.py)
    nfl_a_scores.csv       the table

Scores: VMAF (0-100, closest to what eyes see; needs ffmpeg with libvmaf), SSIM and PSNR.
"floor" = the broadcast with ordinary scaling. "ceiling" = 720p with NO compression damage.
"gap closed" = how much of the floor-to-ceiling distance a mode recovers.

Notes:
  * Sources at 30 fps or less are played frame-by-frame at 59.94 (slow motion), so every
    frame is real. Movement between frames is then about twice what a true 60 fps
    broadcast has: a slightly harder test than real life.
  * Scores reward being faithful to the original. Sharpening and grain can look better
    on a TV yet score lower. Judge those by eye.
"""
import argparse, csv, json, re, subprocess, sys, tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
QDIR = HERE / "quality"
FPS = "60000/1001"
RETIME = "settb=1/60000,setpts=N*1001"      # exact 59.94 timestamps from the frame number
WEAVE = "bwdif=mode=send_field:parity=tff:deint=all"     # 1080i -> 1080p59.94, one frame per field
SIDE = ["-export_side_data", "venc_params"]   # lets spp/pp see the MPEG-2 quantizer


def sh(cmd, **kw):
    return subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, **kw)


def probe(path):
    r = sh(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
            "stream=width,height,avg_frame_rate,codec_name", "-of", "json", str(path)])
    s = json.loads(r.stdout)["streams"][0]
    a, b = s["avg_frame_rate"].split("/")
    s["fps"] = float(a) / float(b) if float(b) else 0.0
    return s


def count_frames(path):
    r = sh(["ffprobe", "-v", "error", "-select_streams", "v:0", "-count_packets",
            "-show_entries", "stream=nb_read_packets", "-of", "csv=p=0", str(path)])
    return int(r.stdout.split()[0].strip(","))


def has_vmaf():
    return "libvmaf" in sh(["ffmpeg", "-hide_banner", "-filters"]).stdout


# ---------------------------------------------------------------- prepare
def cmd_prepare(a):
    src = Path(a.source)
    if not src.exists():
        sys.exit(f"No such file: {src}")
    QDIR.mkdir(exist_ok=True)
    info = probe(src)
    ref, ts = QDIR / f"{a.name}_ref.mkv", QDIR / f"{a.name}.ts"
    pick = "select=not(mod(n\\,2))," if info["fps"] > 90 else ""
    note = ("every other frame kept" if pick else
            "played frame-by-frame at 59.94 (slow motion)" if info["fps"] < 45 else "as is")
    shrink = f"scale=-2:{a.ref_height}:flags=lanczos," if info["height"] > a.ref_height else ""
    if shrink:
        note += f", reference shrunk to {a.ref_height}p"
    print(f"Source: {src.name}  {info['width']}x{info['height']} {info['fps']:.2f} fps  -> {note}")

    r = sh(["ffmpeg", "-v", "error", "-y", "-ss", f"{a.start:g}", "-t", f"{a.seconds:g}", "-i", str(src),
            "-an", "-vf", f"{pick}{RETIME},{shrink}format=yuv420p", "-r", FPS,
            "-c:v", "libx264", "-preset", "fast", "-crf", "6",
            "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709", str(ref)])
    if r.returncode:
        sys.exit(f"Reference failed:\n{r.stderr[-800:]}")
    n = count_frames(ref)
    print(f"  {ref.name}: {n} frames ({n / 59.94:.1f} s)")

    # The simulated broadcast, shaped like the real recordings (one I-frame per 60, three
    # B-frames between P-frames), two passes to hit the bitrate.
    #   720p:  MPEG-2 at 4.9 Mbps, like the local FOX station (clip_720p.ts)
    #   1080i: 59.94 pictures a second woven into 29.97 interlaced frames (top field first),
    #          MPEG-2 at 9.35 Mbps, like the local CBS station (clip_1080i.ts)
    mbps = a.mbps or (9.35 if a.format == "1080i" else 4.9)
    kbps = int(mbps * 1000)
    log = str(Path(tempfile.mkdtemp()) / "2pass")
    if a.format == "1080i":
        if info["height"] < 1080:
            sys.exit("A 1080i test clip needs a source of at least 1080p.")
        shape = ["-vf", "scale=1920:1080:flags=lanczos,interlace=scan=tff,setfield=tff,format=yuv420p",
                 "-r", "30000/1001", "-flags", "+ildct+ilme", "-maxrate", "15000k", "-bufsize", "9000k"]
    else:
        shape = ["-vf", "scale=1280:720:flags=lanczos,format=yuv420p", "-r", FPS,
                 "-maxrate", "12000k", "-bufsize", "7000k"]
    base = ["ffmpeg", "-v", "error", "-y", "-i", str(ref), "-an", *shape,
            "-c:v", "mpeg2video", "-b:v", f"{kbps}k",
            "-g", "60", "-bf", "3", "-mbd", "rd", "-trellis", "2", "-passlogfile", log]
    for p, out in ((1, ["-f", "null", "-"]), (2, ["-f", "mpegts", str(ts)])):
        r = sh(base + ["-pass", str(p)] + out)
        if r.returncode:
            sys.exit(f"MPEG-2 pass {p} failed:\n{r.stderr[-800:]}")
    r = sh(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "packet=size",
            "-of", "csv=p=0", str(ts)])
    bits = sum(int(x.split(",")[0]) for x in r.stdout.split() if x.strip(",").isdigit()) * 8
    print(f"  {ts.name}: {a.format} MPEG-2 at {bits / (n / 59.94) / 1e6:.1f} Mbps (asked {mbps:g})")
    print(f"\nNext:  python3 quality_bench.py score {a.name}")


# ---------------------------------------------------------------- score
def measure(dist_in, dist_chain, ref, w, h, n, d_skip=0, r_skip=0, small=False, vmaf=False):
    """Compare n frames. Returns dict(vmaf, ssim, psnr). dist_in = ffmpeg input args."""
    size = "480:270" if small else f"{w}:{h}"
    d = (f"{dist_chain + ',' if dist_chain else ''}trim=start_frame={d_skip}:end_frame={d_skip + n},"
         f"{RETIME},scale={size}:flags=bicubic,format=yuv420p")
    rr = (f"trim=start_frame={r_skip}:end_frame={r_skip + n},{RETIME},"
          f"{'scale=480:270:flags=bicubic,' if small else ''}format=yuv420p")
    if small:
        g = f"[0:v]{d}[d];[1:v]{rr}[r];[d][r]psnr"
    else:
        g = (f"[0:v]{d}[d];[1:v]{rr},split={3 if vmaf else 2}[r1][r2]{'[r3]' if vmaf else ''};"
             f"[d][r1]ssim[d2];[d2][r2]psnr" + ("[d3];[d3][r3]libvmaf=n_threads=8" if vmaf else ""))
    r = sh(["ffmpeg", "-hide_banner", "-nostats"] + dist_in + ["-i", str(ref),
            "-lavfi", g, "-f", "null", "-"])
    if r.returncode:
        raise RuntimeError(r.stderr[-600:])
    out = {}
    for key, pat in (("vmaf", r"VMAF score: ([\d.]+)"), ("ssim", r"SSIM .*All:([\d.]+)"),
                     ("psnr", r"PSNR .*average:([\d.]+|inf)")):
        m = re.search(pat, r.stderr)
        if m:
            out[key] = float(m.group(1))
    return out


def best_offset(dist_in, chain, ref, n):
    """Processed files can be a frame or two off. Try small shifts, keep the best match."""
    best = (None, -1.0)
    for off in range(-3, 4):
        try:
            p = measure(dist_in, chain, ref, 0, 0, min(n, 90), max(off, 0), max(-off, 0), small=True)["psnr"]
        except (RuntimeError, KeyError):
            continue
        if p > best[1]:
            best = (off, p)
    return best[0] or 0


def cmd_score(a):
    ref, ts = QDIR / f"{a.name}_ref.mkv", QDIR / f"{a.name}.ts"
    if not ref.exists() or not ts.exists():
        sys.exit(f"Run 'prepare' first: {ref.name} / {ts.name} not found in quality/")
    info = probe(ref)
    w, h = info["width"], info["height"]
    interlaced = probe(ts)["height"] == 1080        # a 1080i test clip (see prepare --format)
    n = count_frames(ref) - 8 if interlaced else min(count_frames(ref), count_frames(ts)) - 8
    vmaf = has_vmaf()
    if not vmaf:
        print("This ffmpeg has no libvmaf, so only SSIM and PSNR are shown.\n")

    if interlaced:
        # What the upscaler does today for 1080i: bwdif turns each field into a full frame.
        jobs = [("floor: broadcast, bwdif (today)", ["-i", str(ts)], WEAVE, True),
                ("ceiling: 1080i, no compression", ["-i", str(ref)], f"interlace=scan=tff,{WEAVE}", True)]
    else:
        jobs = [("floor: broadcast, plain scaling", ["-i", str(ts)], "", False),
                ("ceiling: 720p, no compression", ["-i", str(ref)], "scale=1280:720:flags=lanczos", False)]
    for t in a.tries or []:
        label, _, chain = t.partition("=")
        jobs.append((f"try: {label}", SIDE + ["-i", str(ts)], chain, interlaced))
    for f in sorted(QDIR.glob(f"{a.name}_*")):      # every processed version of this clip
        if f.suffix not in (".mp4", ".mkv") or f.stem.endswith("_ref"):
            continue
        label = f.stem[len(a.name) + 1:]
        jobs.append((label[:-3] if label.endswith("_4k") else label, ["-i", str(f)], "", True))

    rows = []
    for label, dist_in, chain, is_file in jobs:
        off, m_n = 0, n
        try:
            if is_file:
                if not chain:
                    m_n = min(n, count_frames(dist_in[-1]) - 8)
                off = best_offset(dist_in, chain, ref, m_n)
            m = measure(dist_in, chain, ref, w, h, m_n - abs(off), max(off, 0), max(-off, 0), vmaf=vmaf)
        except RuntimeError as e:
            print(f"  {label}: failed\n{e}")
            continue
        m.update(label=label, offset=off, frames=m_n - abs(off))
        rows.append(m)
        print(f"  measured {label}" + (f" (shifted {off:+d} frames)" if off else ""), flush=True)

    key = "vmaf" if vmaf else "ssim"
    floor = next((r[key] for r in rows if r["label"].startswith("floor")), None)
    ceil = next((r[key] for r in rows if r["label"].startswith("ceiling")), None)
    print(f"\n{a.name}: compared at {w}x{h}, {n} frames\n")
    print(f"{'version':46} {'VMAF':>6} {'SSIM':>7} {'PSNR':>6}  gap closed")
    for r in sorted(rows, key=lambda r: -r.get(key, 0)):
        gap = ""
        if floor is not None and ceil is not None and ceil > floor and not r["label"].startswith(("floor", "ceiling")):
            gap = f"{100 * (r[key] - floor) / (ceil - floor):+5.0f}%"
        r["gap_closed"] = gap.strip()
        v = f"{r['vmaf']:6.1f}" if "vmaf" in r else "     -"
        print(f"{r['label']:46} {v} {r.get('ssim', 0):7.4f} {r.get('psnr', 0):6.2f}  {gap}")
    out = QDIR / f"{a.name}_scores.csv"
    with open(out, "w", newline="") as f:
        wr = csv.DictWriter(f, ["label", "vmaf", "ssim", "psnr", "gap_closed", "offset", "frames"])
        wr.writeheader()
        wr.writerows(rows)
    print(f"\nSaved {out.relative_to(HERE)}")


def cmd_run(a):
    ts = QDIR / f"{a.name}.ts"
    if not ts.exists():
        sys.exit(f"Run 'prepare' first: {ts} not found")
    extra = [x for x in a.bench_args if x != "--"]
    r = subprocess.run([sys.executable, str(HERE / "ai_upscale_bench.py"), "run", str(ts)] + extra)
    if r.returncode:
        sys.exit("ai_upscale_bench.py stopped with an error; nothing scored.")
    cmd_score(a)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("prepare", help="cut a clean reference and make the simulated broadcast")
    p.add_argument("source")
    p.add_argument("--name", required=True)
    p.add_argument("--start", type=float, default=0)
    p.add_argument("--seconds", type=float, default=20, help="seconds of SOURCE to use")
    p.add_argument("--ref-height", type=int, default=1080, help="shrink bigger sources to this height")
    p.add_argument("--format", choices=("720p", "1080i"), default="720p", help="kind of broadcast to simulate")
    p.add_argument("--mbps", type=float, help="MPEG-2 bitrate (default: 4.9 for 720p, 9.35 for 1080i, as measured)")
    for name, hlp in (("score", "score every version that exists"), ("run", "run ai_upscale_bench.py, then score")):
        s = sub.add_parser(name, help=hlp)
        s.add_argument("name")
        s.add_argument("--try", dest="tries", action="append", metavar="LABEL=FILTERS",
                       help="also score an ffmpeg filter chain applied to the broadcast")
        if name == "run":
            s.add_argument("bench_args", nargs=argparse.REMAINDER, help="after --, options for ai_upscale_bench.py")
    a = ap.parse_args()
    {"prepare": cmd_prepare, "score": cmd_score, "run": cmd_run}[a.cmd](a)


if __name__ == "__main__":
    main()
