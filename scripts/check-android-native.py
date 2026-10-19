#!/usr/bin/env python3
"""Verify the packaged JNI boundary, rather than just successful native linking."""

import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import zipfile


def check(apk):
    readelf = shutil.which("readelf")
    if not readelf:
        ndk = Path(os.environ.get("ANDROID_NDK_HOME", "/nonexistent"))
        readelf = next(ndk.glob("toolchains/llvm/prebuilt/*/bin/llvm-readelf"), None)
    if not readelf:
        raise RuntimeError("readelf or the Android NDK's llvm-readelf is required")

    with zipfile.ZipFile(apk) as archive, tempfile.TemporaryDirectory() as directory:
        for abi in ("arm64-v8a", "x86_64"):
            for library, required, forbidden in (
                (
                    "hev-socks5-tunnel",
                    {"hev_socks5_tunnel_main_from_str", "hev_socks5_tunnel_quit"},
                    {"JNI_OnLoad"},
                ),
                (
                    "multidns-tun",
                    {
                        "Java_org_multidns_app_NativeTunnel_run",
                        "Java_org_multidns_app_NativeTunnel_stop",
                    },
                    set(),
                ),
            ):
                path = Path(directory) / f"{abi}-{library}.so"
                path.write_bytes(archive.read(f"lib/{abi}/lib{library}.so"))
                output = subprocess.check_output(
                    [str(readelf), "--dyn-syms", "--wide", str(path)], text=True
                )
                symbols = {
                    fields[7].split("@")[0]
                    for line in output.splitlines()
                    if len(fields := line.split()) >= 8 and fields[6] != "UND"
                }
                if missing := required - symbols:
                    raise RuntimeError(f"{abi}/{library}: missing exports {sorted(missing)}")
                if unexpected := forbidden & symbols:
                    raise RuntimeError(
                        f"{abi}/{library}: upstream JNI registration must be excluded: "
                        f"{sorted(unexpected)}"
                    )
            print(f"PASS {abi}: HEV C API and MultiDNS JNI bridge")


if __name__ == "__main__":
    try:
        check(sys.argv[1])
    except (IndexError, KeyError, OSError, RuntimeError, subprocess.CalledProcessError) as error:
        sys.exit(f"Android native verification failed: {error}")
