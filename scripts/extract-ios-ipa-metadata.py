#!/usr/bin/env python3
"""Extract exact AltStore source metadata from a built iOS IPA."""

from __future__ import annotations

import argparse
import json
import plistlib
import re
import zipfile
from pathlib import Path
from typing import Any


CODE_SIGN_ENTITLEMENTS = re.compile(
    r'"?CODE_SIGN_ENTITLEMENTS(?:\[[^\]\r\n]+\])?"?\s*=\s*'
    r'("[^"\r\n]*"|[^;\r\n]*?)(?=;|\r?$)',
    re.MULTILINE,
)


def require_string(info: dict[str, Any], key: str) -> str:
    value = info.get(key)
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"Info.plist {key} must be a non-empty string")
    return value


def resolve_entitlements_path(project_root: Path, raw_path: str) -> Path:
    value = raw_path.strip().strip('"')
    if not value:
        raise ValueError("generated Xcode project has an empty CODE_SIGN_ENTITLEMENTS path")
    value = value.replace("$(SRCROOT)/", "").replace("${SRCROOT}/", "")
    value = value.replace("$(PROJECT_DIR)/", "").replace("${PROJECT_DIR}/", "")
    if "$(" in value or "${" in value:
        raise ValueError(f"unsupported variable in CODE_SIGN_ENTITLEMENTS path: {raw_path}")

    result = (project_root / value).resolve()
    try:
        result.relative_to(project_root.resolve())
    except ValueError as error:
        raise ValueError(f"CODE_SIGN_ENTITLEMENTS escapes the Xcode project: {raw_path}") from error
    if not result.is_file():
        raise ValueError(f"referenced entitlements file does not exist: {value}")
    return result


def read_project_entitlements(project_root: Path) -> list[str]:
    if not project_root.is_dir():
        raise ValueError(f"generated Xcode project does not exist: {project_root}")

    project_files = list(project_root.rglob("project.pbxproj"))
    if not project_files:
        raise ValueError(f"no project.pbxproj found below generated Xcode project: {project_root}")
    settings_files = project_files + list(project_root.rglob("*.xcconfig"))

    entitlement_files: set[Path] = set()
    for settings_file in settings_files:
        contents = settings_file.read_text(encoding="utf-8")
        parsed_spans: list[tuple[int, int]] = []
        for match in CODE_SIGN_ENTITLEMENTS.finditer(contents):
            parsed_spans.append(match.span())
            raw_path = match.group(1).strip().strip('"')
            if not raw_path:
                continue
            entitlement_files.add(resolve_entitlements_path(project_root, raw_path))

        # Any occurrence not covered by the parser is an assignment form we
        # do not understand. Fail closed rather than interpreting it as an
        # entitlement-free target.
        masked = list(contents)
        for start, end in parsed_spans:
            masked[start:end] = " " * (end - start)
        if "CODE_SIGN_ENTITLEMENTS" in "".join(masked):
            raise ValueError(f"unparsed CODE_SIGN_ENTITLEMENTS setting in {settings_file}")

        # Xcode capabilities can synthesize signing entitlements without a
        # CODE_SIGN_ENTITLEMENTS file. Refuse to guess if one appears.
        for capabilities in re.finditer(r"SystemCapabilities\s*=\s*\{(.*?)\};", contents, re.DOTALL):
            if capabilities.group(1).strip():
                raise ValueError(
                    "generated Xcode project declares SystemCapabilities; "
                    "explicit AltStore entitlement extraction is required"
                )

    entitlement_keys: set[str] = set()
    for entitlement_file in entitlement_files:
        try:
            entitlements = plistlib.loads(entitlement_file.read_bytes())
        except plistlib.InvalidFileException as error:
            raise ValueError(f"invalid entitlements plist: {entitlement_file}") from error
        if not isinstance(entitlements, dict):
            raise ValueError(f"entitlements plist root must be a dictionary: {entitlement_file}")
        if not all(isinstance(key, str) for key in entitlements):
            raise ValueError(f"entitlements plist keys must be strings: {entitlement_file}")
        entitlement_keys.update(entitlements)

    return sorted(entitlement_keys)


def extract_metadata(ipa_path: Path, xcode_project_root: Path) -> dict[str, Any]:
    with zipfile.ZipFile(ipa_path) as archive:
        plist_paths = [
            name
            for name in archive.namelist()
            if re.fullmatch(r"Payload/[^/]+\.app/Info\.plist", name)
        ]
        if len(plist_paths) != 1:
            raise ValueError(
                f"IPA must contain exactly one top-level app Info.plist; found {len(plist_paths)}"
            )

        extension_plists = [
            name
            for name in archive.namelist()
            if re.fullmatch(r"Payload/[^/]+\.app/PlugIns/[^/]+\.appex/Info\.plist", name)
        ]
        if extension_plists:
            raise ValueError(
                "IPA contains app extensions; AltStore permission extraction must be extended "
                "before this release can be published"
            )

        info = plistlib.loads(archive.read(plist_paths[0]))
        if not isinstance(info, dict):
            raise ValueError("Info.plist root must be a dictionary")

    privacy: dict[str, str] = {}
    for key, value in sorted(info.items()):
        if not isinstance(key, str) or not re.fullmatch(r"NS[A-Za-z0-9]+UsageDescription", key):
            continue
        if not isinstance(value, str) or not value.strip():
            raise ValueError(f"Info.plist {key} must be a non-empty string")
        privacy[key] = value

    entitlements = read_project_entitlements(xcode_project_root)

    return {
        "schemaVersion": 1,
        "bundleIdentifier": require_string(info, "CFBundleIdentifier"),
        "version": require_string(info, "CFBundleShortVersionString"),
        "buildVersion": require_string(info, "CFBundleVersion"),
        "minOSVersion": require_string(info, "MinimumOSVersion"),
        "entitlements": entitlements,
        "privacy": privacy,
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("ipa", type=Path)
    parser.add_argument("--xcode-project-root", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()

    metadata = extract_metadata(args.ipa, args.xcode_project_root)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(metadata, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, zipfile.BadZipFile) as error:
        raise SystemExit(str(error)) from error
