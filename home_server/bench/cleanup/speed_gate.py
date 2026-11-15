#!/usr/bin/env python3
"""Will a LARGE cleanup network keep up with live TV? Time it before training it.

    cd ~/homey/home_server/bench && source .venv-coreml/bin/activate
    python3 cleanup/speed_gate.py

The large network is 2.6x the medium one (look1). Its SPEED does not depend on what it
was trained on, so this builds a random-weight large network, exports it to Core ML exactly
as export_coreml.py would, and times it on the Neural Engine at the sizes the live paths use:

    720p frame   1280x720  -> cleaned at 59.94 a second  (FOX path)
    1080i field  1920x540  -> cleaned at 59.94 a second  (CBS path; two fields per frame)

It prints how much of every second the cleanup step would take. Cleanup runs on the Neural
Engine in its own process, overlapping the GPU upscale (the 1080i bottleneck, ~72% of each
second). Rule of thumb: under ~70% here = comfortable; 70-90% = marginal, test a full game;
over 100% = will not keep up. Compare with the medium figures time_1080i.py prints for look1.
"""
import statistics, sys, tempfile, time
from pathlib import Path
import numpy as np

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from cleanup_model import build, SIZES


def median_ms(fn, warm=8, runs=50):
    for _ in range(warm):
        fn()
    t = []
    for _ in range(runs):
        s = time.perf_counter()
        fn()
        t.append(1000 * (time.perf_counter() - s))
    return statistics.median(t)


def main():
    import torch, coremltools as ct
    frames = 3
    mcfg = {"frames": frames, **SIZES["large"]}
    model = build("t", mcfg).eval()
    params = sum(p.numel() for p in model.parameters())
    print(f"large network: {params:,} numbers to learn "
          f"(medium look1 has ~194,000).  Timing on the Neural Engine ...\n")
    sizes = (("720p frame",  (360, 640), 59.94),
             ("1080i field", (270, 960), 59.94))
    for label, (h, w), rate in sizes:
        shape = (1, 6 * frames, h, w)
        traced = torch.jit.trace(model, torch.rand(*shape))
        ml = ct.convert(traced, inputs=[ct.TensorType(name="x", shape=shape)], convert_to="mlprogram",
                        compute_precision=ct.precision.FLOAT16, minimum_deployment_target=ct.target.macOS13)
        path = str(Path(tempfile.mkdtemp()) / "net.mlpackage")
        ml.save(path)
        net = ct.models.MLModel(path, compute_units=ct.ComputeUnit.CPU_AND_NE)
        x = np.random.rand(*shape).astype(np.float32)
        ms = median_ms(lambda: net.predict({"x": x}))
        pct = ms * rate / 10
        verdict = "comfortable" if pct < 70 else "marginal - test a full game" if pct < 100 else "WILL NOT KEEP UP"
        print(f"  {label:12} {ms:6.1f} ms each x {rate:.0f}/s = {pct:4.0f}% of every second   [{verdict}]")
    print("\nMedium (look1), for comparison, from time_1080i.py:  720p ~5 ms = 30%,  field ~5 ms = 30%.")
    print("If both lines above are comfortable, train the large network (see the handoff).")


if __name__ == "__main__":
    main()
