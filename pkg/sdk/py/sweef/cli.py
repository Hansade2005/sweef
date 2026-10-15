"""Console entry points. ``pip install sweef-browser`` (or ``pipx install``) puts a ``sweef`` command on PATH:
it resolves the engine (PATH, SWEEF_BIN, cache, automatic download) and runs it with all arguments passed through.
``sweef-install`` / ``python -m sweef`` download the engine ahead of time and print where it is."""

import os
import subprocess
import sys
from typing import List, Optional

from ._install import SweefInstallError, cached_binary_path, engine_version, lookup_binary, ensure_binary


def main(argv: Optional[List[str]] = None) -> int:
    args = sys.argv[1:] if argv is None else argv
    try:
        exe = ensure_binary()
    except (SweefInstallError, FileNotFoundError) as e:
        print(f"sweef: {e}", file=sys.stderr)
        return 1
    if os.name == "posix":
        try:
            os.execv(exe, [exe, *args])
        except OSError as e:
            print(f"sweef: {e}", file=sys.stderr)
            return 127
    try:
        return subprocess.call([exe, *args])
    except OSError as e:
        print(f"sweef: {e}", file=sys.stderr)
        return 127


def install_main(argv: Optional[List[str]] = None) -> int:
    args = sys.argv[1:] if argv is None else argv
    args = [a for a in args if a != "install"]
    if "-h" in args or "--help" in args:
        print("usage: sweef-install [--print-path]\n"
              "  Ensures the sweef engine is installed (PATH, SWEEF_BIN or the per-user cache, else downloads it)\n"
              "  and prints its path.\n"
              "  --print-path  print the path only; never download (exit 1 when absent)")
        return 0
    if "--print-path" in args:
        p = lookup_binary()
        if not p:
            print(f"sweef: engine {engine_version()} is not installed (would be {cached_binary_path()}); run sweef-install",
                  file=sys.stderr)
            return 1
        print(p)
        return 0
    try:
        print(ensure_binary())
    except (SweefInstallError, FileNotFoundError) as e:
        print(f"sweef: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
