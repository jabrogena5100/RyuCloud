#!/usr/bin/env python3
"""Check that backend/go.mod's dependencies fit the Go version it declares.

Why: `go mod tidy` fails with "requires go >= X" when any dependency needs a newer Go than the
toolchain in the Docker image (GOTOOLCHAIN=local, so Go will not fetch another one). This script
finds such dependencies *before* you run tidy, and can suggest the newest release of a module
that does fit.

It only reads text from the public Go module proxy (go.mod files, version lists). It installs
nothing and executes no downloaded code. Needs Python 3.8+ and network access; no Go needed.

Usage (repository root):
    python3 scripts/check-go-deps.py                  # check the pins in backend/go.mod
    python3 scripts/check-go-deps.py --suggest PATH   # newest release of PATH that fits, e.g.
                                                      #   --suggest github.com/redis/go-redis/v9

The check uses the full, unpruned module graph, which is stricter than what Go itself loads, so a
failure here is worth reading but is not always fatal; a pass is reliable.
"""
import argparse
import re
import sys
import urllib.request
from functools import cmp_to_key, lru_cache

PROXY = "https://proxy.golang.org"


def escape(path):
    return re.sub(r"[A-Z]", lambda m: "!" + m.group(0).lower(), path)


@lru_cache(maxsize=None)
def fetch(url):
    request = urllib.request.Request(url, headers={"User-Agent": "ryucloud-go-deps-check"})
    with urllib.request.urlopen(request, timeout=30) as response:
        return response.read().decode()


def parse_version(v):
    m = re.match(r"v(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.\-]+))?", v)
    if not m:
        return (0, 0, 0, (1,))
    pre = m[4]
    pre_key = (1,) if pre is None else (0, tuple((0, int(x), "") if x.isdigit() else (1, 0, x) for x in pre.split(".")))
    return (int(m[1]), int(m[2]), int(m[3]), pre_key)


def compare(a, b):
    ka, kb = parse_version(a), parse_version(b)
    return (ka > kb) - (ka < kb)


def go_directive(text):
    m = re.search(r"^go\s+(\d+)\.(\d+)(?:\.(\d+))?", text, re.M)
    return (int(m[1]), int(m[2]), int(m[3] or 0)) if m else (1, 0, 0)


def requirements(text):
    found = []
    for block in re.findall(r"^require\s*\((.*?)^\)", text, re.M | re.S):
        for line in block.splitlines():
            line = line.split("//")[0].strip()
            if line:
                path, version = line.split()[:2]
                found.append((path.strip('"'), version))
    for path, version in re.findall(r"^require[ \t]+([^\s(]\S*)[ \t]+(\S+)", text, re.M):
        found.append((path.strip('"'), version))
    return found


@lru_cache(maxsize=None)
def module_file(path, version):
    text = fetch(f"{PROXY}/{escape(path)}/@v/{version}.mod")
    return go_directive(text), tuple(requirements(text))


def select(roots):
    """Minimum version selection over the whole graph: highest required version of every module."""
    chosen = {}
    queue = list(roots.items())
    while queue:
        path, version = queue.pop()
        if path in chosen and compare(chosen[path], version) >= 0:
            continue
        try:
            _, reqs = module_file(path, version)
        except Exception:
            continue  # very old test-only requirements may be unavailable; they cannot need a new Go
        chosen[path] = version
        queue.extend(reqs)
    return chosen


def fmt(go):
    return ".".join(map(str, go))


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--gomod", default="backend/go.mod")
    parser.add_argument("--suggest", metavar="MODULE")
    args = parser.parse_args()

    text = open(args.gomod).read()
    limit = go_directive(text)
    print(f"{args.gomod} declares go {fmt(limit)}")

    if args.suggest:
        versions = [v for v in fetch(f"{PROXY}/{escape(args.suggest)}/@v/list").split() if "-" not in v]
        versions.sort(key=cmp_to_key(compare), reverse=True)
        for v in versions:
            try:
                go, _ = module_file(args.suggest, v)
            except Exception:
                continue
            if go <= limit:
                print(f"newest {args.suggest} whose own go line fits: {v} (go {fmt(go)})")
                return 0
        print("no release fits")
        return 1

    roots = dict(requirements(text))
    chosen = select(roots)
    too_new = {}
    for path, version in chosen.items():
        go, _ = module_file(path, version)
        if go > limit:
            too_new[path] = (version, go)
    print(f"checked {len(chosen)} modules in the dependency graph")
    if not too_new:
        print(f"OK: nothing needs a Go newer than {fmt(limit)}")
        return 0
    print(f"PROBLEM: these modules need a newer Go than {fmt(limit)}:")
    for path, (version, go) in sorted(too_new.items()):
        print(f"  {path} {version}  needs go {fmt(go)}")
    print("Pin an older release in backend/go.mod (see --suggest MODULE), or raise Go in go.mod and the Dockerfile together.")
    return 1


if __name__ == "__main__":
    sys.exit(main())
