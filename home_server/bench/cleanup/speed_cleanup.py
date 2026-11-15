#!/usr/bin/env python3
"""How fast is each network size on THIS Mac? Speed does not depend on training, so
this runs before training and picks the biggest size that still fits live TV.

    python3 speed_cleanup.py                 (about a minute; writes runs/speed.json)
    python3 speed_cleanup.py --soak 10       (then run the picked size at live-TV pace for
                                              10 minutes, to see what heat does on the Air)
    python3 speed_cleanup.py --budget 7      (stricter limit in ms per frame; default 9)

It times one 720p frame (with its two neighbors for v2) two ways:
  PyTorch   on the Mac's GPU: how training and apply_cleanup.py run it.
  Core ML   Apple's runtime, which is how the live version would run it. Tried on
            "all" (Apple decides), "gpu" and "neural engine". Needs:  pip install coremltools
            These numbers include copying the frame in and out through Python,
            so the live version should be a little faster, not slower.
Live TV allows 16.7 ms per frame in total; the upscaler needs about 5-6 of that,
which is why the default budget for cleanup is 9 ms.
"""
import argparse, json, statistics, tempfile, time
from pathlib import Path
import numpy as np
import torch
from cleanup_model import SIZES, CleanupNet, CleanupNetT, pick_device

HERE = Path(__file__).resolve().parent
H2, W2 = 360, 640


def sync(dev):
    if dev.type == "mps":
        torch.mps.synchronize()


def time_torch(model, shape, dev, iters=150):
    model = model.to(dev).eval()
    x = torch.rand(*shape, device=dev)
    with torch.no_grad():
        for _ in range(20):
            model(x)
        sync(dev)
        t = time.perf_counter()
        for _ in range(iters):
            y = model(x)
        sync(dev)
        float(y[0, 0, 0, 0])                       # make sure the work really finished
    return 1000 * (time.perf_counter() - t) / iters


def to_coreml(model, shape, folder):
    import coremltools as ct
    traced = torch.jit.trace(model.cpu().eval(), torch.rand(*shape))
    ml = ct.convert(traced, inputs=[ct.TensorType(name="x", shape=shape)], convert_to="mlprogram",
                    compute_precision=ct.precision.FLOAT16, minimum_deployment_target=ct.target.macOS13)
    path = str(Path(folder) / "net.mlpackage")
    ml.save(path)
    return path


def coreml_units():
    import coremltools as ct
    return {"all": ct.ComputeUnit.ALL, "gpu": ct.ComputeUnit.CPU_AND_GPU, "neural engine": ct.ComputeUnit.CPU_AND_NE}


def time_coreml(path, shape, unit, iters=100):
    import coremltools as ct
    m = ct.models.MLModel(path, compute_units=coreml_units()[unit])
    inp = {"x": np.random.rand(*shape).astype(np.float32)}
    for _ in range(10):
        m.predict(inp)
    t = time.perf_counter()
    for _ in range(iters):
        m.predict(inp)
    return 1000 * (time.perf_counter() - t) / iters


def soak(run_one, minutes):
    """Run at live-TV pace (59.94 frames a second) and watch for slowing as the Mac warms."""
    tick, per_min, cur, late = 1001 / 60000, [], [], 0
    start = nxt = time.perf_counter()
    while time.perf_counter() - start < minutes * 60:
        t = time.perf_counter()
        run_one()
        ms = 1000 * (time.perf_counter() - t)
        cur.append(ms)
        late += ms > 1000 * tick
        nxt += tick
        if (pause := nxt - time.perf_counter()) > 0:
            time.sleep(pause)
        else:
            nxt = time.perf_counter()              # fell behind; do not try to catch up
        if len(cur) >= 3596:                       # one minute of frames
            per_min.append(statistics.median(cur))
            print(f"  minute {len(per_min):2d}: {per_min[-1]:5.1f} ms per frame (typical), "
                  f"slowest {max(cur):5.1f}", flush=True)
            cur = []
    if per_min:
        change = 100 * (per_min[-1] / per_min[0] - 1)
        print(f"\nFirst minute {per_min[0]:.1f} ms, last minute {per_min[-1]:.1f} ms ({change:+.0f}%). "
              f"Frames slower than 16.7 ms: {late}.")


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--budget", type=float, default=9.0)
    ap.add_argument("--frames", type=int, default=3)
    ap.add_argument("--soak", type=float, default=0, metavar="MINUTES")
    ap.add_argument("--size", choices=list(SIZES), help="soak this size instead of the picked one")
    a = ap.parse_args()
    dev = pick_device()
    try:
        import coremltools as ct
        units = list(coreml_units())
        print(f"PyTorch {torch.__version__} on {dev}, coremltools {ct.__version__}\n")
    except Exception as e:
        units = []
        print(f"PyTorch {torch.__version__} on {dev}. Core ML not tested ({type(e).__name__}: {e}).\n"
              f"For Core ML numbers:  pip install coremltools\n")

    nets = [("v1 (one frame)", CleanupNet(32, 4), (1, 6, H2, W2))]
    nets += [(f"v2 {k}", CleanupNetT(a.frames, **v), (1, 6 * a.frames, H2, W2)) for k, v in SIZES.items()]
    tmp = tempfile.mkdtemp()
    results, paths = {}, {}
    print(f"{'network':16} {'PyTorch':>8} " + " ".join(f"{u:>14}" for u in units) + "   (ms per frame)")
    for label, net, shape in nets:
        row = {"pytorch": round(time_torch(net, shape, dev), 2)}
        if units:
            try:
                paths[label] = to_coreml(net, shape, tempfile.mkdtemp(dir=tmp))
                for u in units:
                    try:
                        row[u] = round(time_coreml(paths[label], shape, u), 2)
                    except Exception as e:
                        row[u] = None
                        print(f"  ({label}, {u}: {type(e).__name__}: {str(e)[:120]})")
            except Exception as e:
                print(f"  (Core ML conversion failed for {label}: {type(e).__name__}: {str(e)[:200]})")
        results[label] = row
        print(f"{label:16} {row['pytorch']:8.1f} " +
              " ".join(f"{row[u]:14.1f}" if row.get(u) else f"{'-':>14}" for u in units), flush=True)

    def best(row):                                 # fastest Core ML way, or PyTorch if none
        got = {u: row[u] for u in units if row.get(u)}
        return min(got.items(), key=lambda kv: kv[1]) if got else ("pytorch", row["pytorch"])

    pick = None
    for k in SIZES:                                # sizes are listed smallest first
        if best(results[f"v2 {k}"])[1] <= a.budget:
            pick = k
    if pick:
        way, ms = best(results[f"v2 {pick}"])
        print(f"\nPick: '{pick}' ({ms:.1f} ms via {way}; the limit is {a.budget:g} ms).")
    else:
        pick = next(iter(SIZES))
        way, ms = best(results[f"v2 {pick}"])
        print(f"\nNothing fits {a.budget:g} ms. Using '{pick}' ({ms:.1f} ms via {way}) so quality can still "
              f"be tested; it would need a faster Mac, or a smaller network, to run live.")
    (HERE / "runs").mkdir(exist_ok=True)
    (HERE / "runs" / "speed.json").write_text(json.dumps(
        {"pick": pick, "way": way, "ms": ms, "budget": a.budget, "frames": a.frames, "results": results}, indent=1))
    print("Saved runs/speed.json (train_cleanup.py --size auto uses this pick).")

    if a.soak:
        size = a.size or pick
        label, shape = f"v2 {size}", (1, 6 * a.frames, H2, W2)
        way = best(results[label])[0]
        print(f"\nRunning '{size}' via {way} at live-TV pace for {a.soak:g} minutes ...")
        if way == "pytorch":
            net = CleanupNetT(a.frames, **SIZES[size]).to(dev).eval()
            x = torch.rand(*shape, device=dev)
            def run_one():
                with torch.no_grad():
                    float(net(x)[0, 0, 0, 0])
        else:
            import coremltools as ct
            m = ct.models.MLModel(paths[label], compute_units=coreml_units()[way])
            inp = {"x": np.random.rand(*shape).astype(np.float32)}
            run_one = lambda: m.predict(inp)
        soak(run_one, a.soak)


if __name__ == "__main__":
    main()
