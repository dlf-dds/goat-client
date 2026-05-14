#!/usr/bin/env python3
"""CLI for managing TestFlight builds + beta tester groups.

Replaces the App Store Connect click-through for:
  - listing builds + their processing state
  - creating internal tester groups
  - adding testers by email
  - attaching builds to a group
  - submitting export-compliance answers

Usage:
  manage_testers.py status [--bundle-id ID]
  manage_testers.py wait-build [--bundle-id ID] [--timeout 1800]
  manage_testers.py setup --email EMAIL [--group NAME] [--bundle-id ID]
  manage_testers.py add-tester --email EMAIL [--group NAME]
  manage_testers.py attach-build BUILD_ID [--group NAME]
  manage_testers.py compliance BUILD_ID --exempt
"""

from __future__ import annotations

import argparse
import sys
import time
from typing import Any

from asc_api import ASCClient, ASCError

DEFAULT_BUNDLE_ID = "io.dlf-dds.goat-client"
DEFAULT_GROUP_NAME = "Internal pilot"


def _app(client: ASCClient, bundle_id: str) -> dict[str, Any]:
    record = client.find_app(bundle_id)
    if record is None:
        sys.exit(f"error: no App Store Connect app record for bundle id {bundle_id!r}")
    return record


def _group(client: ASCClient, app_id: str, name: str, create_if_missing: bool = False) -> dict[str, Any]:
    for g in client.beta_groups(app_id):
        if g.get("attributes", {}).get("name") == name:
            return g
    if create_if_missing:
        return client.create_beta_group(app_id, name, internal=True)
    sys.exit(f"error: beta group {name!r} not found. Use `setup` to create it.")


def cmd_status(args: argparse.Namespace) -> None:
    client = ASCClient()
    app = _app(client, args.bundle_id)
    app_id = app["id"]
    name = app["attributes"]["name"]
    print(f"App: {name}  (id={app_id}, bundle={args.bundle_id})\n")
    print("Builds (newest first):")
    builds = client.list_builds(app_id, limit=args.limit)
    if not builds:
        print("  (none)")
    for b in builds:
        a = b["attributes"]
        state = a.get("processingState", "?")
        encr = a.get("usesNonExemptEncryption")
        compliance = "exempt" if encr is False else "set-to-nonexempt" if encr else "needs-answer"
        print(f"  {b['id']:<12}  v{a.get('version'):<6}  uploaded={a.get('uploadedDate', '?')[:19]}  state={state:<10}  export={compliance}")

    print("\nBeta tester groups:")
    groups = client.beta_groups(app_id)
    if not groups:
        print("  (none)")
    for g in groups:
        ga = g["attributes"]
        print(f"  {g['id']:<10}  {ga.get('name'):<24}  internal={ga.get('isInternalGroup')}  created={(ga.get('createdDate') or '')[:19]}")


def cmd_wait_build(args: argparse.Namespace) -> None:
    client = ASCClient()
    app = _app(client, args.bundle_id)
    app_id = app["id"]
    print(f"Waiting up to {args.timeout}s for a VALID build on {args.bundle_id} ...")
    start = time.time()
    while True:
        builds = client.list_builds(app_id, limit=5)
        if builds:
            top = builds[0]
            a = top["attributes"]
            state = a.get("processingState", "?")
            elapsed = int(time.time() - start)
            print(f"  [{elapsed:>4}s] build {top['id']} v{a.get('version')} state={state}")
            if state == "VALID":
                print(f"==> build {top['id']} is VALID. Hand off to attach-build.")
                return
            if state in {"INVALID", "FAILED"}:
                sys.exit(f"build {top['id']} failed processing: state={state}")
        if time.time() - start > args.timeout:
            sys.exit("error: timeout waiting for build")
        time.sleep(15)


def cmd_setup(args: argparse.Namespace) -> None:
    """Composed action: ensure group exists + add tester + attach latest VALID build + submit compliance."""
    client = ASCClient()
    app = _app(client, args.bundle_id)
    app_id = app["id"]

    # 1. Ensure group exists.
    group = _group(client, app_id, args.group, create_if_missing=True)
    group_id = group["id"]
    print(f"==> group {args.group!r} → id={group_id}")

    # 2. Add tester to the team (idempotent — 409 on duplicate is OK).
    tester = client.add_internal_tester(app_id, args.email)
    if tester is None:
        print(f"==> tester {args.email} already exists; skipping create")
    else:
        print(f"==> tester {args.email} → id={tester['id']}")
        # Attach to group
        try:
            client.attach_tester_to_group(group_id, tester["id"])
            print(f"==> attached tester to group {args.group!r}")
        except ASCError as e:
            if e.status not in (409, 422):
                raise
            print(f"==> tester already in group (HTTP {e.status})")

    # 3. Find latest VALID build + attach + submit compliance.
    valid = [b for b in client.list_builds(app_id, limit=5) if b["attributes"].get("processingState") == "VALID"]
    if not valid:
        print("warn: no VALID builds yet. Run `wait-build` first or upload one.")
        return
    build = valid[0]
    build_id = build["id"]
    print(f"==> latest VALID build: {build_id} v{build['attributes'].get('version')}")

    encr = build["attributes"].get("usesNonExemptEncryption")
    if encr is None:
        client.set_export_compliance(build_id, uses_non_exempt_encryption=False)
        print(f"==> export compliance set to exempt (Mass Market Open Source — wireguard-go + standard TLS)")
    else:
        print(f"==> export compliance already set (usesNonExemptEncryption={encr})")

    try:
        client.attach_build_to_group(group_id, build_id)
        print(f"==> attached build to group {args.group!r}")
    except ASCError as e:
        if e.status in (409, 422):
            print(f"==> build already in group (HTTP {e.status})")
        else:
            raise

    print(f"\nDone. {args.email} will receive a TestFlight invite when Apple's backend propagates (~1-5 min).")


def cmd_add_tester(args: argparse.Namespace) -> None:
    client = ASCClient()
    app = _app(client, args.bundle_id)
    app_id = app["id"]
    group = _group(client, app_id, args.group, create_if_missing=False)

    tester = client.add_internal_tester(app_id, args.email)
    if tester is None:
        print(f"tester {args.email} already exists; attaching to group")
        # Look up tester id
        resp = client.get("/v1/betaTesters", **{"filter[email]": args.email})
        if not resp.get("data"):
            sys.exit(f"error: tester {args.email} exists but couldn't be found by email filter")
        tester = resp["data"][0]

    try:
        client.attach_tester_to_group(group["id"], tester["id"])
        print(f"attached {args.email} to group {args.group!r}")
    except ASCError as e:
        if e.status in (409, 422):
            print(f"already in group (HTTP {e.status})")
        else:
            raise


def cmd_attach_build(args: argparse.Namespace) -> None:
    client = ASCClient()
    app = _app(client, args.bundle_id)
    group = _group(client, app["id"], args.group, create_if_missing=False)
    client.attach_build_to_group(group["id"], args.build_id)
    print(f"attached build {args.build_id} to group {args.group!r}")


def cmd_compliance(args: argparse.Namespace) -> None:
    client = ASCClient()
    client.set_export_compliance(args.build_id, uses_non_exempt_encryption=not args.exempt)
    print(f"build {args.build_id} export compliance: usesNonExemptEncryption={not args.exempt}")


def main() -> None:
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--bundle-id", default=DEFAULT_BUNDLE_ID, help="iOS bundle ID (default: %(default)s)")
    sub = p.add_subparsers(dest="cmd", required=True)

    s = sub.add_parser("status", help="show app, builds, beta groups")
    s.add_argument("--limit", type=int, default=20)
    s.set_defaults(func=cmd_status)

    s = sub.add_parser("wait-build", help="poll until a VALID build appears")
    s.add_argument("--timeout", type=int, default=1800)
    s.set_defaults(func=cmd_wait_build)

    s = sub.add_parser("setup", help="one-shot: create group + add tester + attach latest VALID build + submit compliance")
    s.add_argument("--email", required=True)
    s.add_argument("--group", default=DEFAULT_GROUP_NAME)
    s.set_defaults(func=cmd_setup)

    s = sub.add_parser("add-tester", help="add a tester email to an existing group")
    s.add_argument("--email", required=True)
    s.add_argument("--group", default=DEFAULT_GROUP_NAME)
    s.set_defaults(func=cmd_add_tester)

    s = sub.add_parser("attach-build", help="attach a specific build to a group")
    s.add_argument("build_id")
    s.add_argument("--group", default=DEFAULT_GROUP_NAME)
    s.set_defaults(func=cmd_attach_build)

    s = sub.add_parser("compliance", help="set export compliance answer on a build")
    s.add_argument("build_id")
    s.add_argument("--exempt", action="store_true", help="mark as exempt (Mass Market Open Source)")
    s.set_defaults(func=cmd_compliance)

    args = p.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
