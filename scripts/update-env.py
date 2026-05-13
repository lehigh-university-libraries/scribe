#!/usr/bin/env python3
"""Atomically set one NAME=value entry in a dotenv-style file."""

import argparse
import base64
import os
import re
import tempfile


NAME_RE = re.compile(r"^[A-Z_][A-Z0-9_]*$")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("path")
    parser.add_argument("name")
    parser.add_argument("--base64", dest="encoded", required=True)
    return parser.parse_args()


def main() -> None:
    args = parse_args()
    if not NAME_RE.fullmatch(args.name):
        raise SystemExit(f"invalid env name: {args.name}")

    value = base64.b64decode(args.encoded, validate=True).decode("utf-8")
    if "\x00" in value or "\n" in value or "\r" in value:
        raise SystemExit(f"{args.name} contains characters that are invalid in .env values")

    next_line = f"{args.name}={value}\n"
    existing = []
    if os.path.exists(args.path):
        with open(args.path, "r", encoding="utf-8") as handle:
            existing = handle.readlines()

    changed = False
    output = []
    prefix = f"{args.name}="
    for line in existing:
        if line.startswith(prefix):
            if not changed:
                output.append(next_line)
                changed = True
            continue
        output.append(line)
    if not changed:
        output.append(next_line)

    directory = os.path.dirname(os.path.abspath(args.path)) or "."
    fd, tmp_path = tempfile.mkstemp(prefix=".env.", dir=directory, text=True)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.writelines(output)
        os.replace(tmp_path, args.path)
    except Exception:
        try:
            os.unlink(tmp_path)
        except FileNotFoundError:
            pass
        raise


if __name__ == "__main__":
    main()
