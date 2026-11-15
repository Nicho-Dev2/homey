#!/usr/bin/env python3
"""Turn a trained cleanup network into a Core ML file, the form the live version runs.

    python3 export_coreml.py look1                  -> runs/look1/cleanup.mlpackage        (720p frames)
    python3 export_coreml.py look1 --format 1080i   -> runs/look1/cleanup_1080i.mlpackage  (1080i fields, 1920x540)

Needs the .venv-coreml environment (Python 3.12 with torch and coremltools).
The trained weights work at any picture size, but a Core ML file is fixed to one: a
720p frame, or one field of a 1080i frame (each with its neighbors for v2 networks).
It uses 16-bit numbers, which is what the Neural Engine runs. After saving, it runs the same
random input through PyTorch and Core ML and reports the biggest difference, in
brightness steps out of 255 (under 1 means the two agree to within rounding).
"""
import argparse, json
from pathlib import Path
import numpy as np
import torch
from cleanup_model import build

HERE = Path(__file__).resolve().parent
PICTURE = {"720p": (1280, 720), "1080i": (1920, 540)}      # what the network is shown: width, height


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("run")
    ap.add_argument("--format", choices=list(PICTURE), default="720p")
    a = ap.parse_args()
    import coremltools as ct

    run = HERE / "runs" / a.run
    ckpt = torch.load(run / "model.pt", map_location="cpu")
    arch = ckpt.get("arch", "v1")
    frames = ckpt["model"].get("frames", 1) if arch == "t" else 1
    model = build(arch, ckpt["model"]).eval()
    model.load_state_dict(ckpt["state"])
    width, height = PICTURE[a.format]
    shape = (1, 6 * frames, height // 2, width // 2)       # the network works at half size (see cleanup_model.pack)
    suffix = "" if a.format == "720p" else f"_{a.format}"

    torch.manual_seed(0)
    x = torch.rand(*shape)
    traced = torch.jit.trace(model, x)
    ml = ct.convert(traced, inputs=[ct.TensorType(name="x", shape=shape)], convert_to="mlprogram",
                    compute_precision=ct.precision.FLOAT16, minimum_deployment_target=ct.target.macOS13)
    path = run / f"cleanup{suffix}.mlpackage"
    ml.save(str(path))

    with torch.no_grad():
        want = model(x).numpy()
    got = next(iter(ct.models.MLModel(str(path), compute_units=ct.ComputeUnit.CPU_AND_NE)
                    .predict({"x": x.numpy()}).values()))
    diff = float(np.abs(got - want).max() * 255)
    (run / f"coreml{suffix}.json").write_text(json.dumps({"input": "x", "shape": shape, "frames": frames,
                                                 "max_difference_of_255": round(diff, 3)}, indent=1))
    print(f"\nSaved {path}")
    print(f"Biggest difference from PyTorch: {diff:.2f} of 255 "
          f"({'fine' if diff < 1.5 else 'larger than expected; tell Claude'})")
    if a.format == "720p":
        print(f"Next:  python3 apply_cleanup.py {a.run} --all --coreml")
    else:
        print(f"Next:  cp -R {path} {HERE.parent.parent / 'enhance' / 'models'}/{a.run}_{a.format}.mlpackage")


if __name__ == "__main__":
    main()
