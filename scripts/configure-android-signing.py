#!/usr/bin/env python3
"""Add release signing to a Tauri-generated Android Gradle project.

Tauri regenerates src-tauri/gen/android, so the release workflow runs this script
after `tauri android init` rather than committing generated Gradle files. Signing
values are read directly from the build process environment so passwords are not
serialized through Java's lossy .properties format.
"""

from __future__ import annotations

import argparse
from pathlib import Path

ENVIRONMENT_BLOCK = '''
fun requiredSigningEnvironment(name: String): String =
    System.getenv(name)?.takeIf { it.isNotEmpty() }
        ?: error("Missing required Android signing environment variable: $name")

'''

SIGNING_CONFIG = '''    signingConfigs {
        create("release") {
            keyAlias = requiredSigningEnvironment("ANDROID_UPLOAD_KEY_ALIAS")
            keyPassword = requiredSigningEnvironment("ANDROID_UPLOAD_KEY_PASSWORD")
            storeFile = rootProject.file(requiredSigningEnvironment("ANDROID_UPLOAD_KEYSTORE_PATH"))
            storePassword = requiredSigningEnvironment("ANDROID_UPLOAD_KEYSTORE_PASSWORD")
        }
    }
'''


def configure(build_file: Path) -> None:
    source = build_file.read_text(encoding="utf-8")

    if "requiredSigningEnvironment" in source:
        raise SystemExit(f"{build_file} is already configured for release signing")

    android_anchor = "android {\n"
    if source.count(android_anchor) != 1:
        raise SystemExit(f"Could not find a unique Android block in {build_file}")
    source = source.replace(android_anchor, ENVIRONMENT_BLOCK + android_anchor, 1)

    build_types_anchor = "    buildTypes {\n"
    if source.count(build_types_anchor) != 1:
        raise SystemExit(f"Could not find a unique buildTypes block in {build_file}")
    source = source.replace(build_types_anchor, SIGNING_CONFIG + build_types_anchor, 1)

    release_anchor = '        getByName("release") {\n'
    if source.count(release_anchor) != 1:
        raise SystemExit(f"Could not find a unique release build type in {build_file}")
    source = source.replace(
        release_anchor,
        release_anchor + '            signingConfig = signingConfigs.getByName("release")\n',
        1,
    )

    build_file.write_text(source, encoding="utf-8")


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("build_file", type=Path)
    args = parser.parse_args()
    configure(args.build_file)


if __name__ == "__main__":
    main()
