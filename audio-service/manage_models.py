"""Operator CLI for public model prewarming and disposable cache management."""

from __future__ import annotations

import argparse
import json
import os

from embedding_cache import cache
from model_runtime import runtime


def main() -> int:
    """Download public weights only after an explicit operator command."""
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=["prewarm", "check", "status", "clear-cache"])
    arguments = parser.parse_args()
    models = runtime()
    if arguments.command == "prewarm":
        success = models.prewarm(
            download=True, progress=lambda state: print(json.dumps(state), flush=True)
        )
        return 0 if success else 1
    if arguments.command == "check":
        success = models.prewarm(download=False)
        print(json.dumps(models.status()))
        return 0 if success else 1
    if arguments.command == "clear-cache":
        cache(models.settings.cache_dir).clear()
    print(json.dumps(models.status()))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
