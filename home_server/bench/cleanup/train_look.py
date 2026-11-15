#!/usr/bin/env python3
"""Step 2b: "look-focused" training. Takes a network already trained for accuracy
(train_cleanup.py) and keeps training it to produce pictures that LOOK like clean
footage: real texture instead of smooth areas.

    pip install torchvision                          (once; brings the texture judge)
    python3 train_look.py --name look1 --from v3 --data data_v3
    python3 train_look.py --name look0 --from v3 --data data_v3 --gan 0     (no critic: safer, milder)
    python3 apply_cleanup.py look1 --all

It follows the published Real-ESRGAN recipe (open source), with our small network:
  pixel   x1.0  stay close to the clean picture, pixel by pixel
  texture x1.0  have the same edges and texture as the clean picture, judged by the first
                layers of VGG-19, a standard image network from PyTorch's model library.
                Downloaded once (about 550 MB); the part we use is then saved to
                judge/vgg19_features.pt and later runs need no internet.
  critic  x0.1  a second network learns to tell cleaned patches from clean ones, and the
                cleanup network learns to fool it. The critic is not kept.

The scores (VMAF, PSNR) reward accuracy and may go DOWN here while the picture looks
better. Judge the result by eye on the TV, and watch for: invented detail on jersey
numbers and score graphics, and shimmer in grass or crowds from frame to frame.

Writes runs/<name>/model.pt in the same format as train_cleanup.py.
"""
import argparse, csv, json, math, time
from pathlib import Path
import numpy as np
import torch
import torch.nn as nn
import torch.nn.functional as F
from torch.nn.utils import spectral_norm
from cleanup_model import build, pack, pack_t, unpack, pick_device

HERE = Path(__file__).resolve().parent
JUDGE = HERE / "judge" / "vgg19_features.pt"
TAPS = {2: 0.1, 7: 0.1, 16: 1.0, 25: 1.0, 34: 1.0}    # conv1_2, conv2_2, conv3_4, conv4_4, conv5_4 (before ReLU)


def to_rgb(y, uv):
    """Video-range BT.709 Y (B,1,H,W) + UV (B,2,H/2,W/2), floats 0..1 -> RGB (B,3,H,W), about 0..1."""
    uv = F.interpolate(uv, scale_factor=2, mode="bilinear", align_corners=False)
    yy = (y * 255 - 16) / 219
    cb = (uv[:, 0:1] * 255 - 128) / 224
    cr = (uv[:, 1:2] * 255 - 128) / 224
    return torch.cat([yy + 1.5748 * cr, yy - 0.1873 * cb - 0.4681 * cr, yy + 1.8556 * cb], dim=1)


class TextureJudge(nn.Module):
    """The first layers of VGG-19, fixed. Compares two pictures by their features."""
    def __init__(self):
        super().__init__()
        import torchvision
        layers = torchvision.models.vgg19(weights=None).features[:35]
        if JUDGE.exists():
            layers.load_state_dict(torch.load(JUDGE, map_location="cpu"))
        else:
            print("Downloading VGG-19 once (about 550 MB) ...", flush=True)
            full = torchvision.models.vgg19(weights=torchvision.models.VGG19_Weights.IMAGENET1K_V1)
            layers.load_state_dict(full.features[:35].state_dict())
            JUDGE.parent.mkdir(exist_ok=True)
            torch.save(layers.state_dict(), JUDGE)
            print(f"Saved the part we use to {JUDGE.relative_to(HERE)} (no download next time).")
        self.layers = layers.eval()
        for p in self.parameters():
            p.requires_grad_(False)
        self.register_buffer("mean", torch.tensor([0.485, 0.456, 0.406]).view(1, 3, 1, 1))
        self.register_buffer("std", torch.tensor([0.229, 0.224, 0.225]).view(1, 3, 1, 1))

    def features(self, rgb):
        x, out = (rgb - self.mean) / self.std, []
        for i, layer in enumerate(self.layers):
            x = F.relu(x) if isinstance(layer, nn.ReLU) else layer(x)   # not in place: taps stay intact
            if i in TAPS:
                out.append(x)
        return out

    def distance(self, a_rgb, b_rgb):
        with torch.no_grad():
            fb = self.features(b_rgb)
        return sum(w * F.l1_loss(fa, f) for w, fa, f in zip(TAPS.values(), self.features(a_rgb), fb))


class Critic(nn.Module):
    """U-Net critic with spectral normalization, as in Real-ESRGAN (narrower: 32 base channels).
    Gives a real/cleaned verdict for every pixel."""
    def __init__(self, nf=32):
        super().__init__()
        sn = spectral_norm
        self.c0 = nn.Conv2d(3, nf, 3, 1, 1)
        self.c1 = sn(nn.Conv2d(nf, nf * 2, 4, 2, 1, bias=False))
        self.c2 = sn(nn.Conv2d(nf * 2, nf * 4, 4, 2, 1, bias=False))
        self.c3 = sn(nn.Conv2d(nf * 4, nf * 8, 4, 2, 1, bias=False))
        self.c4 = sn(nn.Conv2d(nf * 8, nf * 4, 3, 1, 1, bias=False))
        self.c5 = sn(nn.Conv2d(nf * 4, nf * 2, 3, 1, 1, bias=False))
        self.c6 = sn(nn.Conv2d(nf * 2, nf, 3, 1, 1, bias=False))
        self.c7 = sn(nn.Conv2d(nf, nf, 3, 1, 1, bias=False))
        self.c8 = sn(nn.Conv2d(nf, nf, 3, 1, 1, bias=False))
        self.c9 = nn.Conv2d(nf, 1, 3, 1, 1)

    def forward(self, x):
        act = lambda t: F.leaky_relu(t, 0.2)
        up = lambda t: F.interpolate(t, scale_factor=2, mode="bilinear", align_corners=False)
        x0 = act(self.c0(x))
        x1 = act(self.c1(x0))
        x2 = act(self.c2(x1))
        x3 = act(self.c3(x2))
        x4 = act(self.c4(up(x3))) + x2
        x5 = act(self.c5(up(x4))) + x1
        x6 = act(self.c6(up(x5))) + x0
        return self.c9(act(self.c8(act(self.c7(x6)))))


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--name", required=True)
    ap.add_argument("--from", dest="start", required=True, help="accuracy-trained run to start from, e.g. v3")
    ap.add_argument("--data", required=True)
    ap.add_argument("--epochs", type=int, default=8)
    ap.add_argument("--batch", type=int, default=24)
    ap.add_argument("--lr", type=float, default=1e-4)
    ap.add_argument("--pixel", type=float, default=1.0)
    ap.add_argument("--texture", type=float, default=1.0)
    ap.add_argument("--gan", type=float, default=0.1, help="critic weight; 0 = no critic")
    a = ap.parse_args()

    data = Path(a.data) if Path(a.data).is_absolute() else HERE / a.data
    meta = json.loads((data / "meta.json").read_text())
    cfg, n, T = meta["config"], meta["patches"], meta.get("frames", 1)
    ckpt = torch.load(HERE / "runs" / a.start / "model.pt", map_location="cpu")
    arch, mcfg = ckpt.get("arch", "v1"), ckpt["model"]
    if (mcfg.get("frames", 1) if arch == "t" else 1) != T:
        raise SystemExit(f"Run '{a.start}' and {data.name}/ use a different number of frames.")
    seed = cfg["seed"]
    dmg = {k: np.load(data / f"dmg_{k}.npy", mmap_mode="r") for k in ("y", "uv")}
    clean = {k: np.load(data / f"clean_{k}.npy", mmap_mode="r") for k in ("y", "uv")}

    rng = np.random.default_rng(seed)
    torch.manual_seed(seed)
    order = rng.permutation(n)                      # same held-back patches as train_cleanup.py
    n_val = min(2000, max(32, n // 25))
    val_idx, train_idx = np.sort(order[:n_val]), order[n_val:]
    dev = pick_device()

    def load(arr, idx):
        return (torch.from_numpy(arr["y"][idx]).to(dev).float().div_(255),
                torch.from_numpy(arr["uv"][idx]).to(dev).float().div_(255))

    def batch_of(idx, mirror=()):
        dy, duv = load(dmg, idx)
        cy, cuv = load(clean, idx)
        for dim in mirror:
            dy, duv, cy, cuv = dy.flip(dim), duv.flip(dim), cy.flip(dim), cuv.flip(dim)
        x = pack_t(dy, duv) if T > 1 else pack(dy.unsqueeze(1), duv)
        mid = (dy[:, T // 2], duv[:, T // 2]) if T > 1 else (dy, duv)
        return x, cy.unsqueeze(1), cuv, mid[0].unsqueeze(1), mid[1]

    net = build(arch, mcfg).to(dev)
    net.load_state_dict(ckpt["state"])
    ema = build(arch, mcfg).to(dev).eval()          # smoothed copy of the weights: this is what gets saved
    ema.load_state_dict(ckpt["state"])
    for p in ema.parameters():
        p.requires_grad_(False)
    judge = TextureJudge().to(dev)
    opt = torch.optim.Adam(net.parameters(), lr=a.lr, betas=(0.9, 0.99))
    critic = opt_c = None
    if a.gan > 0:
        critic = Critic().to(dev)
        opt_c = torch.optim.Adam(critic.parameters(), lr=a.lr, betas=(0.9, 0.99))
    bce = nn.BCEWithLogitsLoss()

    run = HERE / "runs" / a.name
    run.mkdir(parents=True, exist_ok=True)
    (run / "settings.json").write_text(json.dumps(
        {"name": a.name, "kind": "look", "from": a.start, "arch": arch, "model": mcfg, "epochs": a.epochs,
         "batch": a.batch, "lr": a.lr, "weights": {"pixel": a.pixel, "texture": a.texture, "gan": a.gan},
         "seed": seed, "patches": n, "data": data.name, "torch": torch.__version__, "device": str(dev)}, indent=1))
    steps = len(train_idx) // a.batch
    print(f"Starting from '{a.start}'. Weights: pixel {a.pixel:g}, texture {a.texture:g}, critic {a.gan:g}. "
          f"Device: {dev}. {a.epochs} rounds of {steps} steps.")

    def validate(model):
        se = count = tex_d = tex_c = batches = 0.0
        with torch.no_grad():
            for i in range(0, n_val, 48):
                x, cy, cuv, my, muv = batch_of(val_idx[i:i + 48])
                oy, ouv = unpack(model(x))
                real = to_rgb(cy, cuv)
                se += float(((oy.clamp(0, 1) - cy) ** 2).sum())
                count += cy.numel()
                tex_d += float(judge.distance(to_rgb(my, muv), real))
                tex_c += float(judge.distance(to_rgb(oy, ouv), real))
                batches += 1
        return 10 * math.log10(count / max(se, 1e-12)), tex_d / batches, tex_c / batches

    p0, td, tc0 = validate(ema)
    print(f"Held-back patches at the start: accuracy {p0:.2f} dB; texture distance to clean: "
          f"damaged {td:.3f}, cleaned {tc0:.3f} (lower = closer look)\n")
    print("round   pixel    texture  critic(G/D)    accuracy dB   texture dist   minutes")
    t0 = time.time()
    with open(run / "log.csv", "w", newline="") as f:
        log = csv.writer(f)
        log.writerow(["epoch", "pixel", "texture", "gan_g", "gan_d", "psnr", "texture_distance", "minutes"])
        for ep in range(1, a.epochs + 1):
            perm = rng.permutation(train_idx)
            tot = np.zeros(4)
            for s in range(steps):
                idx = np.sort(perm[s * a.batch:(s + 1) * a.batch])
                x, cy, cuv, _, _ = batch_of(idx, [d for d in (-1, -2) if rng.random() < 0.5])
                out = net(x)
                oy, ouv = unpack(out)
                fake, real = to_rgb(oy, ouv), to_rgb(cy, cuv)
                l_pix = F.l1_loss(out, pack(cy, cuv))
                l_tex = judge.distance(fake, real)
                loss = a.pixel * l_pix + a.texture * l_tex
                l_g = l_d = torch.zeros((), device=dev)
                if critic is not None:
                    for p in critic.parameters():
                        p.requires_grad_(False)
                    verdict = critic(fake)
                    l_g = bce(verdict, torch.ones_like(verdict))       # "make the critic say: clean"
                    loss = loss + a.gan * l_g
                opt.zero_grad(set_to_none=True)
                loss.backward()
                opt.step()
                with torch.no_grad():                                   # update the smoothed copy
                    for pe, pn in zip(ema.parameters(), net.parameters()):
                        pe.mul_(0.999).add_(pn, alpha=0.001)
                if critic is not None:
                    for p in critic.parameters():
                        p.requires_grad_(True)
                    v_real, v_fake = critic(real), critic(fake.detach())
                    l_d = bce(v_real, torch.ones_like(v_real)) + bce(v_fake, torch.zeros_like(v_fake))
                    opt_c.zero_grad(set_to_none=True)
                    l_d.backward()
                    opt_c.step()
                tot += [float(l_pix.detach()), float(l_tex.detach()), float(l_g.detach()), float(l_d.detach())]
                if ep == 1 and s == 20:
                    per = (time.time() - t0) / 21
                    print(f"        (about {per * steps * a.epochs / 60:.0f} minutes in total at this speed)", flush=True)
            tot /= steps
            psnr, _, tc = validate(ema)
            mins = (time.time() - t0) / 60
            torch.save({"state": ema.state_dict(), "arch": arch, "model": mcfg, "psnr_cleaned": psnr,
                        "texture_distance": tc, "epoch": ep, "kind": "look", "from": a.start}, run / "model.pt")
            log.writerow([ep, *(f"{v:.5f}" for v in tot), f"{psnr:.3f}", f"{tc:.4f}", f"{mins:.1f}"])
            f.flush()
            print(f"{ep:4d}   {tot[0]:.5f}  {tot[1]:.4f}   {tot[2]:.3f}/{tot[3]:.3f}      {psnr:6.2f}        "
                  f"{tc:.3f}       {mins:6.1f}", flush=True)
    print(f"\nSaved {run / 'model.pt'} (texture distance {tc0:.3f} -> {tc:.3f}, accuracy {p0:.2f} -> {psnr:.2f} dB)")
    print(f"Next:  python3 apply_cleanup.py {a.name} --all     and look at the files on the TV")


if __name__ == "__main__":
    main()
