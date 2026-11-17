#!/usr/bin/env python3
# Copyright 2026 The Kube-burner Authors.
# SPDX-License-Identifier: Apache-2.0
"""Create or version-safely update JSON fixtures without client-side apply."""

import copy
import json
import subprocess
import sys


def merge(current, desired):
    result = copy.deepcopy(current)
    for key, value in desired.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = merge(result[key], value)
        else:
            result[key] = copy.deepcopy(value)
    return result


def update(desired, client=None):
    if client is None:
        client = subprocess.run
    kind = desired["kind"]
    name = desired["metadata"]["name"]
    original_uid = None
    for _ in range(3):
        current = client(["oc", "--request-timeout=30s", "get", kind, name,
                          "--ignore-not-found", "-o", "json"], capture_output=True, text=True)
        if current.returncode:
            raise RuntimeError(f"Cannot read {kind}/{name}")
        if current.stdout.strip():
            obj = json.loads(current.stdout)
            uid = obj["metadata"]["uid"]
            if original_uid is not None and uid != original_uid:
                raise RuntimeError(f"Identity changed for {kind}/{name}")
            original_uid = uid
            body = merge(obj, desired)
            body["metadata"]["uid"] = uid
            body["metadata"]["resourceVersion"] = obj["metadata"]["resourceVersion"]
            body.pop("status", None)
            body["metadata"].pop("managedFields", None)
            operation = "replace"
        else:
            if original_uid is not None:
                raise RuntimeError(f"Resource disappeared: {kind}/{name}")
            body = copy.deepcopy(desired)
            operation = "create"
        result = client(["oc", "--request-timeout=30s", operation, "-f", "-"],
                        input=json.dumps(body), capture_output=True, text=True)
        if result.returncode == 0:
            print(f"{kind}/{name} {operation} succeeded", flush=True)
            return
        if not any(reason in result.stderr for reason in ("Conflict", "AlreadyExists", "object has been modified")):
            raise RuntimeError(f"Cannot {operation} {kind}/{name}; exit={result.returncode}")
    raise RuntimeError(f"Conflicts exhausted for {kind}/{name}")


def documents(raw):
    decoder = json.JSONDecoder()
    while raw.strip():
        value, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        yield from value.get("items", []) if value.get("kind") == "List" else [value]


if __name__ == "__main__":
    for document in documents(sys.stdin.read()):
        if document["kind"] not in ("Agent", "LLMProvider", "ApprovalPolicy"):
            raise SystemExit("Unsupported fixture kind")
        update(document)
