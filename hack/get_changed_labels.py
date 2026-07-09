#!/usr/bin/env python3
"""
Parse bats_test_mappings.yml and return the Bats --filter-tags arguments for the
workloads whose code paths have been modified in the current branch.

The list of changed files is computed by diffing against a base reference (the
DIFF_TARGET environment variable, defaulting to "origin/main"). If a changed
file does not match any of the mapped paths, no filter is returned so that the
whole test suite is executed (safe default for shared code).
"""

import os
import subprocess
import sys
from fnmatch import fnmatch

try:
    import yaml
except ImportError:  # pragma: no cover - fall back to running every test
    print(
        "Warning: PyYAML is not installed, running the whole test suite",
        file=sys.stderr,
    )
    sys.exit(0)


def get_changed_files(repo_root, diff_target):
    """Get the list of files changed against the diff target."""
    # Three-dot diff compares against the merge-base so that only the changes
    # introduced by the current branch are taken into account.
    for ref in (f"{diff_target}...HEAD", diff_target):
        result = subprocess.run(
            ["git", "diff", "--name-only", ref],
            cwd=repo_root,
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode == 0:
            return sorted(
                line.strip() for line in result.stdout.splitlines() if line.strip()
            )
    print(
        f"Warning: unable to compute changed files against '{diff_target}', "
        "running the whole test suite",
        file=sys.stderr,
    )
    return None


def matches_pattern(file_path, pattern):
    """Check whether a file path matches a glob pattern from the mappings."""
    if file_path == pattern:
        return True
    if fnmatch(file_path, pattern):
        return True
    # For directory patterns ending with /*, match any file under that directory
    if pattern.endswith("/*"):
        directory = pattern[:-2]
        if file_path.startswith(directory + "/"):
            return True
    return False


def main():
    if len(sys.argv) != 2:
        print("Usage: get_changed_labels.py <mappings_file>", file=sys.stderr)
        sys.exit(1)
    mappings_file = sys.argv[1]
    if not os.path.exists(mappings_file):
        print(f"Error: {mappings_file} not found", file=sys.stderr)
        sys.exit(1)

    diff_target = os.environ.get("DIFF_TARGET", "origin/main")
    changed_files = get_changed_files(os.getcwd(), diff_target)
    # On error computing the diff, run the whole suite.
    if changed_files is None:
        sys.exit(0)

    try:
        with open(mappings_file, "r") as f:
            data = yaml.safe_load(f)
    except Exception as e:
        print(f"Error: Failed to parse YAML file: {e}", file=sys.stderr)
        sys.exit(1)

    for entry in data:
        if "label" not in entry or "paths" not in entry:
            print(
                "Invalid YAML structure, either label or paths are missing",
                file=sys.stderr,
            )
            sys.exit(1)
        if not isinstance(entry["paths"], list):
            print(
                "Invalid YAML structure, paths should be a list", file=sys.stderr
            )
            sys.exit(1)

    # Map every changed file to its labels. If a changed file is not covered by
    # any mapping, run the whole suite (return nothing).
    labels = set()
    for changed_file in changed_files:
        file_labels = {
            entry["label"]
            for entry in data
            if any(matches_pattern(changed_file, p) for p in entry["paths"])
        }
        if not file_labels:
            sys.exit(0)
        labels.update(file_labels)

    for label in sorted(labels):
        print(f"--filter-tags {label}", end=" ")


if __name__ == "__main__":
    main()
