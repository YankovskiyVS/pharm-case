"""Offline installation/verification of operator-authorized VoiceFM artifacts."""

from __future__ import annotations

import argparse
import importlib
import json
import os
import shutil
import tempfile
from pathlib import Path

from artifacts import ArtifactError, FILES, validate


def install(source: Path, destination: Path) -> None:
    """Verify before copying and after copying; never overwrite an installed model."""
    validate(source)
    if destination.exists() and any(destination.iterdir()):
        raise ArtifactError("destination_not_empty")
    destination.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix="voicefm-install-", dir=destination.parent))
    try:
        for name in FILES | {"manifest.json"}:
            shutil.copyfile(source / name, stage / name)
            (stage / name).chmod(0o644)
        validate(stage)
        stage.chmod(0o755)
        os.replace(stage, destination)
    finally:
        if stage.exists():
            shutil.rmtree(stage)


def convert_checkpoint(source: Path, output: Path) -> None:
    """Explicit local conversion of genuine training checkpoints using restricted loading.

    No allowlisting/custom unpickler is used. Checkpoints rejected by weights_only
    must be exported by their authorized producer; never fall back to unsafe load.
    """
    torch = importlib.import_module("torch")
    safe = importlib.import_module("safetensors.torch")
    state = torch.load(str(source), map_location="cpu", weights_only=True)
    if not isinstance(state, dict):
        raise ArtifactError("unsupported_checkpoint")
    state = state.get("model_state_dict", state)
    if not isinstance(state, dict):
        raise ArtifactError("unsupported_checkpoint")
    if any(k.startswith("audio_encoder.") for k in state):
        state = {
            k.removeprefix("audio_encoder."): v
            for k, v in state.items()
            if k.startswith("audio_encoder.")
        }
    if not state or any(
        not isinstance(k, str) or not isinstance(v, torch.Tensor) for k, v in state.items()
    ):
        raise ArtifactError("unsupported_checkpoint")
    if output.exists():
        raise ArtifactError("destination_not_empty")
    safe.save_file({k: v.detach().cpu().contiguous() for k, v in state.items()}, str(output))


def main() -> int:
    """No automatic downloads or fabricated model manifests."""
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    verify = commands.add_parser("verify")
    verify.add_argument("directory", type=Path)
    copy = commands.add_parser("install")
    copy.add_argument("--source", type=Path, required=True)
    copy.add_argument("--destination", type=Path, required=True)
    convert = commands.add_parser("convert-checkpoint")
    convert.add_argument("--source", type=Path, required=True)
    convert.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        if args.command == "verify":
            manifest = validate(args.directory)
            print(
                json.dumps(
                    {
                        "status": "artifacts_verified",
                        "ready": False,
                        "requires_runtime_load": True,
                        "architecture": manifest.architecture,
                    }
                )
            )
        elif args.command == "install":
            install(args.source, args.destination)
            print(
                json.dumps({"status": "installed", "ready": False, "requires_runtime_load": True})
            )
        else:
            convert_checkpoint(args.source, args.output)
            print(
                json.dumps(
                    {"status": "converted", "ready": False, "requires_bundle_validation": True}
                )
            )
        return 0
    except (ArtifactError, OSError, ValueError, ImportError) as error:
        reason = str(error) if isinstance(error, ArtifactError) else "artifact_operation_failed"
        print(json.dumps({"status": "model_unavailable", "ready": False, "reason_code": reason}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
