"""One isolated CPU model operation; parent enforces a hard wall-clock deadline."""

from __future__ import annotations

import argparse
import json
import subprocess
from pathlib import Path

from adapter import Adapter
from artifacts import ArtifactError, validate


def main() -> int:
    """Run only trusted local code and safetensors; never print audio or raw exceptions."""
    parser = argparse.ArgumentParser()
    parser.add_argument("--bundle", type=Path, required=True)
    parser.add_argument("--input", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--task", default="unknown")
    args = parser.parse_args()
    try:
        model = Adapter(args.bundle, validate(args.bundle))
        outputs = {}
        if args.input:
            canonical = args.input.with_suffix(".wav")
            decoded = subprocess.run(
                [
                    "ffmpeg",
                    "-nostdin",
                    "-v",
                    "error",
                    "-protocol_whitelist",
                    "file,pipe",
                    "-format_whitelist",
                    "wav,ogg,matroska,webm,mov,mp4,m4a,3gp,3g2,mj2,mp3,aac,flac",
                    "-i",
                    str(args.input),
                    "-t",
                    "121",
                    "-c:a",
                    "pcm_f32le",
                    "-y",
                    str(canonical),
                ],
                stdin=subprocess.DEVNULL,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=15,
                check=False,
            )
            if decoded.returncode:
                raise ArtifactError("invalid_audio")
            outputs = model.infer(canonical, args.task)
        args.output.write_text(json.dumps({"ok": True, "outputs": outputs}, allow_nan=False))
        return 0
    except ArtifactError as error:
        args.output.write_text(json.dumps({"ok": False, "reason_code": str(error)}))
        return 1
    except Exception:
        # Native/model loaders raise many provider-specific exception types.
        args.output.write_text(json.dumps({"ok": False, "reason_code": "unsupported_checkpoint"}))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
