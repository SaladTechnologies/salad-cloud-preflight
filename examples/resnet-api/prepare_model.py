"""Fetch official pretrained weights at build time and record their checksum."""
import hashlib
import json
from pathlib import Path

import torch
import torchvision
from torchvision.models import ResNet18_Weights

assert torch.__version__.split("+")[0] == "2.10.0", torch.__version__
assert torchvision.__version__.split("+")[0] == "0.25.0", torchvision.__version__
weights = ResNet18_Weights.IMAGENET1K_V1
state = weights.get_state_dict(progress=False, check_hash=True)
directory = Path("model")
directory.mkdir(exist_ok=True)
path = directory / "resnet18.pt"
torch.save(state, path)
(directory / "metadata.json").write_text(json.dumps({
    "model": "resnet18", "weights": "IMAGENET1K_V1", "source": weights.url,
    "sha256": hashlib.sha256(path.read_bytes()).hexdigest(),
    "categories": weights.meta["categories"],
    "torch": torch.__version__, "torchvision": torchvision.__version__,
}, indent=2))
print("Prepared pretrained ResNet-18 with 1000 ImageNet categories")
