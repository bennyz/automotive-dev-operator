# shellcheck shell=bash

# Adapter between AIB's JSON lockfile and Hermeto's best-effort RPM backend.
# Keep this isolated from the build orchestration so another fetcher can replace it.

aib_lock_is_rpm_only() {
  python3 - "$1" <<'PYEOF'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as lockfile:
    lock = json.load(lockfile)

depsolves = lock.get("depsolves", {})
if not isinstance(depsolves, dict) or not any(
    isinstance(entry, dict) and entry.get("packages") for entry in depsolves.values()
):
    raise SystemExit(1)

non_rpm_sections = ("containers", "embedded_files", "ostree_commits")
raise SystemExit(any(lock.get(section) for section in non_rpm_sections))
PYEOF
}

aib_lock_has_rpms() {
  python3 - "$1" <<'PYEOF'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as lockfile:
    lock = json.load(lockfile)

depsolves = lock.get("depsolves", {})
raise SystemExit(
    not isinstance(depsolves, dict)
    or not any(
        isinstance(entry, dict) and entry.get("packages")
        for entry in depsolves.values()
    )
)
PYEOF
}

convert_aib_lock_to_hermeto() {
  local aib_lock="$1" hermeto_lock="$2" rpm_map="$3"

  python3 - "$aib_lock" "$hermeto_lock" "$rpm_map" <<'PYEOF'
import hashlib
import json
import pathlib
import re
import sys
import urllib.parse

aib_lock_path, hermeto_lock_path, rpm_map_path = map(pathlib.Path, sys.argv[1:])
with aib_lock_path.open(encoding="utf-8") as lockfile:
    lock = json.load(lockfile)

if lock.get("version") != 1:
    raise SystemExit("AIB lockfile version must be 1")

depsolves = lock.get("depsolves")
if not isinstance(depsolves, dict) or not depsolves:
    raise SystemExit("AIB lockfile contains no depsolved RPM packages")

by_arch = {}
url_checksums = {}
for depsolve_key in sorted(depsolves):
    depsolve = depsolves[depsolve_key]
    request = depsolve.get("request", {})
    arch = request.get("architecture")
    if not isinstance(arch, str) or not arch:
        raise SystemExit(f"depsolve {depsolve_key} has no request architecture")

    packages = depsolve.get("packages")
    if not isinstance(packages, list):
        raise SystemExit(f"depsolve {depsolve_key} has no package list")

    arch_packages = by_arch.setdefault(arch, {})
    for package in packages:
        url = package.get("url")
        checksum = package.get("checksum")
        if not isinstance(url, str) or not url:
            raise SystemExit(f"depsolve {depsolve_key} contains an RPM without a URL")
        if urllib.parse.urlsplit(url).scheme not in {"http", "https"}:
            raise SystemExit(f"Hermeto spike supports only HTTP(S) RPM URLs: {url}")
        if not isinstance(checksum, str) or not re.fullmatch(
            r"[A-Za-z0-9_+-]+:[0-9a-f]+", checksum
        ):
            raise SystemExit(f"RPM has no usable checksum: {url}")
        if package.get("secrets"):
            raise SystemExit(f"Hermeto spike does not support secret-backed RPM URLs: {url}")

        previous_checksum = url_checksums.setdefault(url, checksum)
        if previous_checksum != checksum:
            raise SystemExit(f"RPM URL has conflicting checksums: {url}")

        existing = arch_packages.get(checksum)
        if existing is not None:
            if url < existing["url"]:
                existing["url"] = url
                existing["filename"] = pathlib.Path(url).name
            continue

        filename = pathlib.Path(url).name
        if not filename.endswith(".rpm"):
            raise SystemExit(f"RPM URL does not end in .rpm: {url}")
        # Hermeto excludes its synthetic IDs from PURLs, preserving download_url.
        repoid = "hermeto-aib-" + hashlib.sha256(checksum.encode()).hexdigest()[:12]
        arch_packages[checksum] = {
            "url": url,
            "checksum": checksum,
            "repoid": repoid,
            "filename": filename,
            "name": package.get("name"),
            "evr": package.get("evr"),
        }

arches = []
rpm_map = []
for arch in sorted(by_arch):
    packages = []
    for checksum, package in sorted(by_arch[arch].items()):
        lock_entry = {
            "url": package["url"],
            "repoid": package["repoid"],
            "checksum": checksum,
        }
        for optional in ("name", "evr"):
            if package[optional] is not None:
                lock_entry[optional] = package[optional]
        packages.append(lock_entry)
        rpm_map.append(
            {
                "checksum": checksum,
                "path": str(
                    pathlib.Path("deps/rpm")
                    / arch
                    / package["repoid"]
                    / package["filename"]
                ),
                "url": package["url"],
            }
        )
    if packages:
        arches.append({"arch": arch, "packages": packages})

if not arches:
    raise SystemExit("AIB lockfile contains no RPM packages")

# JSON is valid YAML 1.2. Emitting it avoids adding a YAML library to the AIB image.
hermeto_lock = {
    "lockfileVersion": 1,
    "lockfileVendor": "redhat",
    "arches": arches,
}
hermeto_lock_path.write_text(json.dumps(hermeto_lock, indent=2) + "\n", encoding="utf-8")
rpm_map_path.write_text(json.dumps(rpm_map, indent=2) + "\n", encoding="utf-8")
PYEOF
}

map_hermeto_rpms_to_osbuild_store() {
  local rpm_map="$1" hermeto_output="$2" store="$3"

  python3 - "$rpm_map" "$hermeto_output" "$store" <<'PYEOF'
import hashlib
import json
import os
import pathlib
import shutil
import sys

rpm_map_path, output_path, store_path = map(pathlib.Path, sys.argv[1:])
entries = json.loads(rpm_map_path.read_text(encoding="utf-8"))
store_path.mkdir(parents=True, exist_ok=True)

for entry in entries:
    source = output_path / entry["path"]
    if not source.is_file():
        raise SystemExit(f"Hermeto did not fetch {entry['url']} at {source}")

    algorithm, expected = entry["checksum"].split(":", 1)
    try:
        digest = hashlib.new(algorithm)
    except ValueError as error:
        raise SystemExit(f"unsupported checksum algorithm {algorithm}: {error}") from error
    with source.open("rb") as rpm:
        for chunk in iter(lambda: rpm.read(1024 * 1024), b""):
            digest.update(chunk)
    if digest.hexdigest() != expected:
        raise SystemExit(f"checksum mismatch while mapping {entry['url']}")

    destination = store_path / entry["checksum"]
    temporary = destination.with_name(destination.name + f".tmp.{os.getpid()}")
    shutil.copyfile(source, temporary)
    os.replace(temporary, destination)

print(len({entry["checksum"] for entry in entries}))
PYEOF
}

prepare_locked_rpms_with_hermeto() {
  local aib_lock="$1" build_dir="$2" workspace_path="$3" restore_sources_ref="$4"
  local restored_sbom="$build_dir/osbuild_store/hermeto-rpm-bom.json"
  rm -f "$workspace_path/hermeto-rpm-bom.json"
  if [ ! -f "$aib_lock" ]; then
    if [ -n "$restore_sources_ref" ] && [ -f "$restored_sbom" ]; then
      cp "$restored_sbom" "$workspace_path/hermeto-rpm-bom.json"
    elif [ -z "$restore_sources_ref" ]; then
      rm -f "$restored_sbom"
    fi
    return 0
  fi

  if [ "${HERMETO_PREFETCH:-false}" != "true" ]; then
    if [ -n "$restore_sources_ref" ] && [ -f "$restored_sbom" ]; then
      cp "$restored_sbom" "$workspace_path/hermeto-rpm-bom.json"
    else
      rm -f "$restored_sbom"
    fi
    echo "Hermeto RPM prefetch disabled; using AIB source handling"
    return 0
  fi

  if aib_lock_is_rpm_only "$aib_lock"; then
    command -v unshare >/dev/null 2>&1 || fail "RPM-only locked builds require unshare for network isolation"
    # shellcheck disable=SC2034 # Consumed by the concatenated build_image.sh.
    AIB_BUILD_NETWORK_DISABLED=true
  else
    echo "WARNING: AIB lockfile contains non-RPM dependencies; AIB build network remains enabled"
  fi

  if [ -n "$restore_sources_ref" ]; then
    if [ -f "$restored_sbom" ]; then
      cp "$restored_sbom" "$workspace_path/hermeto-rpm-bom.json"
    fi
    echo "Using restored osbuild sources; skipping Hermeto RPM fetch"
    return 0
  fi

  if ! aib_lock_has_rpms "$aib_lock"; then
    rm -f "$restored_sbom"
    echo "AIB lockfile contains no RPMs; skipping Hermeto"
    return 0
  fi

  command -v podman >/dev/null 2>&1 || fail "Hermeto RPM prefetch requires podman"
  command -v python3 >/dev/null 2>&1 || fail "Hermeto RPM prefetch requires python3"

  local input_dir="$build_dir/hermeto-input"
  local output_dir="$build_dir/hermeto-output"
  local hermeto_lock="$input_dir/rpms.lock.yaml"
  local rpm_map="$input_dir/aib-rpm-map.json"
  local source_store="$build_dir/osbuild_store/sources/org.osbuild.files"
  rm -rf "$input_dir" "$output_dir"
  mkdir -p "$input_dir" "$output_dir"
  chmod 0777 "$input_dir" "$output_dir"

  convert_aib_lock_to_hermeto "$aib_lock" "$hermeto_lock" "$rpm_map" \
    || fail "failed to convert AIB lockfile for Hermeto"

  echo "Fetching locked RPMs with Hermeto image $HERMETO_IMAGE"
  local -a certificate_args=()
  if [ -f /etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem ]; then
    certificate_args=(
      -e SSL_CERT_FILE=/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem
      -v /etc/pki/ca-trust:/etc/pki/ca-trust:ro
    )
  fi
  podman run --rm --network=host \
    "${certificate_args[@]}" \
    -v "$input_dir:/source:ro,Z" \
    -v "$output_dir:/output:Z" \
    "$HERMETO_IMAGE" \
    fetch-deps --source /source --output /output rpm \
    || fail "Hermeto RPM fetch failed"

  local mapped_count
  mapped_count=$(map_hermeto_rpms_to_osbuild_store "$rpm_map" "$output_dir" "$source_store") \
    || fail "failed to map Hermeto RPMs into the osbuild source store"
  [ -f "$output_dir/bom.json" ] || fail "Hermeto did not produce bom.json"
  cp "$output_dir/bom.json" "$restored_sbom"
  cp "$output_dir/bom.json" "$workspace_path/hermeto-rpm-bom.json"
  rm -rf "$input_dir" "$output_dir"
  echo "Mapped $mapped_count verified RPMs into the osbuild source store"
}
