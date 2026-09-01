#!/usr/bin/env python3
"""Trivial, unmodified agent.

It makes a plain HTTP GET with NO credential of its own. Run directly it gets a
401. Run through the Keydris proxy, its egress is transparently brokered and the
upstream credential is injected on the wire, so it gets a 200 without ever
holding a secret.
"""
import os
import sys
import urllib.error
import urllib.request

URL = os.environ.get("KEYDRIS_BACKEND_URL", "http://127.0.0.1:8080/")


def main() -> int:
    try:
        with urllib.request.urlopen(URL, timeout=5) as resp:
            status = resp.status
            body = resp.read().decode("utf-8", "replace")
    except urllib.error.HTTPError as e:
        status = e.code
        body = e.read().decode("utf-8", "replace")
    except Exception as e:  # noqa: BLE001 - POC: surface any failure plainly
        print(f"agent: request to {URL} failed: {e}", file=sys.stderr)
        return 2

    print(f"agent: GET {URL} -> {status}")
    print(f"agent: body: {body.strip()}")
    return 0 if status == 200 else 1


if __name__ == "__main__":
    sys.exit(main())
