#!/usr/bin/env python3
"""Fail closed when the security-tools lock cannot drive a verified install."""

import json
import re
import sys
from pathlib import Path

SCHEMA_VERSION = "security-tools-lock/v1"
PLATFORMS = {"linux/amd64", "linux/arm64"}
SHA256 = re.compile(r"^[0-9a-f]{64}$")
HTTPS_URL = re.compile(r"^https://[^\s]+$")
SAFE_BINARY = re.compile(r"^[a-z0-9][a-z0-9._-]*$")


class LockError(ValueError):
    pass


def require_string(value, field, tool_name):
    if not isinstance(value, str) or not value:
        raise LockError(f"{tool_name}: {field} must be a non-empty string")
    return value


def verify_lock(path: Path) -> None:
    try:
        lock = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise LockError(f"cannot read {path}: {error}") from error

    if not isinstance(lock, dict) or lock.get("schema_version") != SCHEMA_VERSION:
        raise LockError(f"schema_version must be {SCHEMA_VERSION!r}")
    tools = lock.get("tools")
    if not isinstance(tools, list) or not tools:
        raise LockError("tools must be a non-empty list")

    names = set()
    for tool in tools:
        if not isinstance(tool, dict):
            raise LockError("each tool must be an object")
        name = require_string(tool.get("name"), "name", "tool")
        if name in names:
            raise LockError(f"duplicate tool name: {name}")
        names.add(name)
        status = tool.get("status")
        if status == "disabled":
            require_string(tool.get("reason"), "reason", name)
            if any(field in tool for field in ("version", "binary", "release_url", "platforms")):
                raise LockError(f"{name}: disabled tools may not define install fields")
            continue
        if status != "enabled":
            raise LockError(f"{name}: status must be enabled or disabled")

        for field in ("version", "binary", "release_url"):
            require_string(tool.get(field), field, name)
        if not SAFE_BINARY.fullmatch(tool["binary"]):
            raise LockError(f"{name}: binary must be a safe file name")
        if not HTTPS_URL.fullmatch(tool["release_url"]):
            raise LockError(f"{name}: release_url must be an https URL")
        if tool["version"] not in tool["release_url"]:
            raise LockError(f"{name}: release_url must identify the pinned version")
        platforms = tool.get("platforms")
        if not isinstance(platforms, dict) or set(platforms) != PLATFORMS:
            raise LockError(f"{name}: platforms must contain exactly {sorted(PLATFORMS)}")
        for platform, artifact in platforms.items():
            if not isinstance(artifact, dict):
                raise LockError(f"{name}: {platform} artifact must be an object")
            asset = require_string(artifact.get("asset"), f"{platform} asset", name)
            digest = require_string(artifact.get("sha256"), f"{platform} sha256", name)
            archive_or_binary = asset.endswith((".tar.gz", ".tar.xz", ".zip", "-update"))
            if not HTTPS_URL.fullmatch(asset) or not archive_or_binary:
                raise LockError(f"{name}: {platform} asset must be an https tar.gz, tar.xz, zip, or updater binary URL")
            if tool["version"] not in asset:
                raise LockError(f"{name}: {platform} asset must identify the pinned version")
            if not SHA256.fullmatch(digest):
                raise LockError(f"{name}: {platform} sha256 must be a lowercase SHA-256 digest")
            binary_digest = artifact.get("binary_sha256")
            if binary_digest is not None and not SHA256.fullmatch(binary_digest):
                raise LockError(f"{name}: {platform} binary_sha256 must be a lowercase SHA-256 digest")


def main(argv: list[str]) -> int:
    path = Path(argv[1]) if len(argv) == 2 else Path("security-tools.lock.json")
    if len(argv) > 2:
        print(f"usage: {argv[0]} [lock-file]", file=sys.stderr)
        return 2
    try:
        verify_lock(path)
    except LockError as error:
        print(f"security tools lock verification failed: {error}", file=sys.stderr)
        return 1
    print(f"security tools lock verified: {path}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
