#!/usr/bin/env python3
"""Step 2: train a cleanup network on the patches from make_pairs.py.

    python3 train_cleanup.py --name v1                                  (one frame, data/)
    python3 train_cleanup.py --name v2 --data data_v2                   (with neighbors; size from speed test)
    python3 train_cleanup.py --name v2s --data data_v2 --size small

The kind of network follows the data: patches with neighboring frames train the v2
network. --size auto (default) uses the size speed_cleanup.py found fast enough for
live TV on this Mac, or "medium" if that test has not been run.

Writes runs/<name>/: model.pt (best so far), log.csv, settings.json.
Each line shows how close the held-back patches are to the clean picture, in dB,
before ("damaged") and after ("cleaned"). Higher is better; +1 dB is clearly visible.
"""
import argparse, csv, json, math, time
from pathlib import Path
import numpy as np
import torch
from cleanup_model import SIZES, build, pack, pack_t, unpack, pick_device

HERE = Path(__file__).resolve().parent


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--name", required=True)
    ap.add_argument("--data", default="data")
    ap.add_argument("--size", default="auto", choices=["auto", *SIZES])
    ap.add_argument("--epochs", type=int)
    ap.add_argument("--batch", type=int)
    ap.add_argument("--lr", type=float)
    a = ap.parse_args()

    data = Path(a.data) if Path(a.data).is_absolute() else HERE / a.data
    meta = json.loads((data / "meta.json").read_text())
    cfg, n, T = meta["config"], meta["patches"], meta.get("frames", 1)
    if T > 1:
        size = a.size
        if size == "auto":
            sp = HERE / "runs" / "speed.json"
            size = json.loads(sp.read_text())["pick"] if sp.exists() else "medium"
        arch, mcfg = "t", {"frames": T, **SIZES[size]}
        label = f"v2 '{size}', {T} frames"
    else:
        arch, mcfg = "v1", dict(cfg["model"])
        label = "v1, one frame"
    epochs = a.epochs or cfg["train"]["epochs"]
    batch = a.batch or cfg["train"]["batch"]
    lr = a.lr or cfg["train"]["lr"]
    seed = cfg["seed"]

    # Patches stay on disk and are read as needed, so big data sets do not fill the memory.
    print(f"Using {n} patches from {data.name}/", flush=True)
    dmg = {k: np.load(data / f"dmg_{k}.npy", mmap_mode="r") for k in ("y", "uv")}
    clean = {k: np.load(data / f"clean_{k}.npy", mmap_mode="r") for k in ("y", "uv")}

    rng = np.random.default_rng(seed)
    torch.manual_seed(seed)
    order = rng.permutation(n)
    n_val = min(2000, max(batch, n // 25))
    val_idx, train_idx = np.sort(order[:n_val]), order[n_val:]
    dev = pick_device()

    def load(arr, idx):                       # uint8 patches -> floats 0..1 on the GPU
        return (torch.from_numpy(arr["y"][idx]).to(dev).float().div_(255),
                torch.from_numpy(arr["uv"][idx]).to(dev).float().div_(255))

    def batch_of(idx, mirror=()):
        dy, duv = load(dmg, idx)              # (B,[T,]P,P) and (B,[T,]2,P/2,P/2)
        cy, cuv = load(clean, idx)            # (B,P,P)     and (B,2,P/2,P/2)
        for dim in mirror:                    # -1 = left/right, -2 = up/down; same for all
            dy, duv, cy, cuv = dy.flip(dim), duv.flip(dim), cy.flip(dim), cuv.flip(dim)
        x = pack_t(dy, duv) if T > 1 else pack(dy.unsqueeze(1), duv)
        center_y = dy[:, T // 2] if T > 1 else dy
        return x, pack(cy.unsqueeze(1), cuv), center_y, cy

    model = build(arch, mcfg).to(dev)
    params = sum(p.numel() for p in model.parameters())
    steps = len(train_idx) // batch
    opt = torch.optim.Adam(model.parameters(), lr=lr)
    sched = torch.optim.lr_scheduler.CosineAnnealingLR(opt, T_max=steps * epochs, eta_min=lr / 100)

    run = HERE / "runs" / a.name
    run.mkdir(parents=True, exist_ok=True)
    (run / "settings.json").write_text(json.dumps(
        {"name": a.name, "arch": arch, "model": mcfg, "epochs": epochs, "batch": batch, "lr": lr, "seed": seed,
         "patches": n, "data": data.name, "data_config": cfg, "torch": torch.__version__, "device": str(dev)}, indent=1))
    print(f"Network: {label}, {params:,} numbers to learn. Device: {dev}. {epochs} rounds of {steps} steps.")

    def validate():
        model.eval()
        se_d = se_c = count = 0.0
        with torch.no_grad():
            for i in range(0, n_val, 64):
                x, _, dy, cy = batch_of(val_idx[i:i + 64])
                oy, _ = unpack(model(x))
                se_d += float(((dy - cy) ** 2).sum())
                se_c += float(((oy.squeeze(1).clamp(0, 1) - cy) ** 2).sum())
                count += cy.numel()
        model.train()
        db = lambda se: 10 * math.log10(count / max(se, 1e-12))
        return db(se_d), db(se_c)

    before, _ = validate()
    print(f"Held-back patches, damaged vs clean: {before:.2f} dB\n")
    print("round   loss     damaged  cleaned   gain    minutes")
    best, t0 = -1e9, time.time()
    with open(run / "log.csv", "w", newline="") as f:
        log = csv.writer(f)
        log.writerow(["epoch", "loss", "psnr_damaged", "psnr_cleaned", "minutes"])
        for ep in range(1, epochs + 1):
            perm = rng.permutation(train_idx)
            running = 0.0
            for s in range(steps):
                idx = np.sort(perm[s * batch:(s + 1) * batch])
                mirror = [d for d in (-1, -2) if rng.random() < 0.5]
                x, target, _, _ = batch_of(idx, mirror)
                loss = torch.sqrt((model(x) - target) ** 2 + 1e-6).mean()
                opt.zero_grad(set_to_none=True)
                loss.backward()
                opt.step()
                sched.step()
                running += float(loss)
            d, c = validate()
            mins = (time.time() - t0) / 60
            star = ""
            if c > best:
                best = c
                torch.save({"state": model.state_dict(), "arch": arch, "model": mcfg, "psnr_damaged": d,
                            "psnr_cleaned": c, "epoch": ep}, run / "model.pt")
                star = " *saved"
            log.writerow([ep, f"{running / steps:.5f}", f"{d:.3f}", f"{c:.3f}", f"{mins:.1f}"])
            f.flush()
            print(f"{ep:4d}   {running / steps:.5f}   {d:6.2f}   {c:6.2f}   {c - d:+5.2f}   {mins:6.1f}{star}", flush=True)
    print(f"\nBest: {best:.2f} dB ({best - before:+.2f} over damaged). Saved {run / 'model.pt'}")
    print(f"Next:  python3 apply_cleanup.py {a.name} --all")


if __name__ == "__main__":
    main()
