#!/usr/bin/env python3
import glob
import json
import os
import sys
from pathlib import Path

import yaml


def prepare(workspace, manifest_path, results):
    source = workspace / ".caib-source"
    root = (source / "repository").resolve()

    def contained(path):
        try:
            resolved = path.resolve(strict=True)
        except (FileNotFoundError, RuntimeError) as exc:
            raise ValueError(f"missing or invalid Git source path: {path}") from exc
        if not resolved.is_relative_to(root):
            raise ValueError(f"source path escapes the repository: {path.relative_to(root)}")
        return resolved

    def reject_lfs(path):
        if path.is_file():
            with path.open("rb") as stream:
                if stream.readline(256).strip() == b"version https://git-lfs.github.com/spec/v1":
                    raise ValueError(f"Git LFS input is not supported: {path.relative_to(root)}")

    for directory, dirs, files in os.walk(root):
        for name in dirs + files:
            entry = Path(directory) / name
            if entry.is_symlink():
                if Path(os.readlink(entry)).is_absolute():
                    raise ValueError("absolute symlinks are not supported in Git build inputs")
                contained(entry)

    manifest = contained(root / manifest_path)
    if not manifest.is_file() or manifest.stat().st_size > 900 * 1024:
        raise ValueError("manifest must be a regular file no larger than 900 KiB")
    content = yaml.safe_load(manifest.read_text())
    if not isinstance(content, dict):
        raise ValueError("manifest must be a YAML mapping")
    target = content.get("target") or "qemu"
    if not isinstance(target, str) or len(target) > 256 or any(c.isspace() for c in target):
        raise ValueError("invalid manifest target")

    qm = content.get("qm") or {}
    if not isinstance(qm, dict):
        raise ValueError("manifest qm must be a mapping")
    for section in (content.get("content"), qm.get("content")):
        if section is not None and not isinstance(section, dict):
            raise ValueError("manifest content must be a mapping")
        add_files = (section or {}).get("add_files") or []
        if not isinstance(add_files, list):
            raise ValueError("manifest add_files must be a list")
        for entry in add_files:
            if not isinstance(entry, dict):
                raise ValueError("manifest add_files entries must be mappings")
            if "text" in entry or "url" in entry:
                continue
            value = entry.get("source_glob", entry.get("source_path", entry.get("source")))
            if value is None:
                continue
            if not isinstance(value, str) or Path(value).is_absolute():
                raise ValueError("Git add_files sources must be repository-relative")
            candidate = manifest.parent / value
            if not candidate.resolve().is_relative_to(root):
                raise ValueError("add_files source escapes the repository")
            matches = glob.glob(str(candidate)) if "source_glob" in entry else [str(candidate)]
            if not matches:
                raise ValueError(f"add_files source matches no files: {value}")
            for match in matches:
                resolved_match = contained(Path(match))
                if resolved_match.is_dir():
                    for nested in resolved_match.rglob("*"):
                        reject_lfs(nested)
                else:
                    reject_lfs(resolved_match)

    lockfile = root / Path(manifest_path).parent / "aib.lock"
    if lockfile.exists() or lockfile.is_symlink():
        lockfile = contained(lockfile)
        if not lockfile.is_file() or not 0 < lockfile.stat().st_size <= 900 * 1024:
            raise ValueError("aib.lock must be a nonempty regular file no larger than 900 KiB")
        lock = json.loads(lockfile.read_text())
        if not isinstance(lock, dict) or type(lock.get("version")) is not int or lock["version"] != 1:
            raise ValueError("aib.lock must be an AIB JSON object with version 1")
        if manifest.stat().st_size + lockfile.stat().st_size > 900 * 1024:
            raise ValueError("manifest and lockfile exceed 900 KiB combined")

    commit = (source / "commit").read_text().strip()
    if len(commit) not in (40, 64) or any(c not in "0123456789abcdef" for c in commit):
        raise ValueError("invalid resolved Git commit")
    (source / "manifest-path").write_text(manifest_path)
    metadata = {"url": os.environ.get("SOURCE_URL", ""), "revision": os.environ.get("SOURCE_REVISION", ""), "commit": commit, "manifestPath": manifest_path}
    (source / "source.json").write_text(json.dumps(metadata))
    (results / "commit").write_text(commit)
    (results / "target").write_text(target)
    print(f"Prepared {manifest_path} at commit {commit}")


if __name__ == "__main__":
    try:
        prepare(Path(os.environ["SOURCE_WORKSPACE"]), os.environ["SOURCE_MANIFEST"], Path("/tekton/results"))
    except (ValueError, OSError, json.JSONDecodeError, yaml.YAMLError) as exc:
        print(f"Git source preparation failed: {exc}", file=sys.stderr)
        sys.exit(1)
