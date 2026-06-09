#!/usr/bin/env bash
set -euo pipefail

CONFIG="${1:-configs/experiments/default.yaml}"
mkdir -p logs runs/example

aexp event param config "$CONFIG"
aexp event note "starting training"

python - "$CONFIG" <<'PY' 2>&1 | tee logs/train.log
import math
import sys
import time

try:
    from aexp_events import metric, note, param, progress
except Exception:
    def metric(*args, **kwargs): pass
    def note(*args, **kwargs): pass
    def param(*args, **kwargs): pass
    def progress(*args, **kwargs): pass

config = sys.argv[1]
param("config", config)
note("training loop started")

epochs = 10
for epoch in range(1, epochs + 1):
    train_loss = math.exp(-epoch / 4.0)
    val_loss = train_loss + 0.05
    val_score = 1.0 - val_loss
    progress("train", epoch, total=epochs)
    metric("train/loss", train_loss, epoch=epoch)
    metric("val/loss", val_loss, epoch=epoch)
    metric("val/score", val_score, epoch=epoch)
    print(f"epoch={epoch} train_loss={train_loss:.4f} val_loss={val_loss:.4f} val_score={val_score:.4f}", flush=True)
    time.sleep(0.1)

note("training loop finished")
PY

aexp event note "finished training"
