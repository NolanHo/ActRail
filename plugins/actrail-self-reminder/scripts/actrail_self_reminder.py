#!/usr/bin/env python3
"""Small CLI for ActRail Scheduler self-reminders."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request
from http.cookies import SimpleCookie
from typing import Any


DEFAULT_BASE_URL = "http://127.0.0.1:18743"


class ActRailError(RuntimeError):
    pass


def parse_duration(value: str) -> int:
    text = value.strip().lower()
    if not text:
        raise argparse.ArgumentTypeError("duration is required")
    if text.isdigit():
        return int(text)
    match = re.fullmatch(r"(\d+)(s|m|h|d)", text)
    if not match:
        raise argparse.ArgumentTypeError("duration must look like 30s, 10m, 2h, 1d, or raw seconds")
    amount = int(match.group(1))
    unit = match.group(2)
    multipliers = {"s": 1, "m": 60, "h": 3600, "d": 86400}
    return amount * multipliers[unit]


def base_url(value: str | None) -> str:
    url = (value or os.environ.get("ACTRAIL_BASE_URL") or DEFAULT_BASE_URL).rstrip("/")
    if not url.startswith(("http://", "https://")):
        raise ActRailError("ACTRAIL_BASE_URL must start with http:// or https://")
    return url


def cookie_header_from_set_cookie(response: Any) -> str:
    values = response.headers.get_all("Set-Cookie") or []
    cookie = SimpleCookie()
    for value in values:
        cookie.load(value)
    return "; ".join(f"{key}={morsel.value}" for key, morsel in cookie.items())


class Client:
    def __init__(self, url: str) -> None:
        self.url = url
        self.cookie = self._env_cookie()
        password = os.environ.get("ACTRAIL_AUTH_PASSWORD")
        if password and not self.cookie:
            self.login(password)

    def _env_cookie(self) -> str:
        raw_header = os.environ.get("ACTRAIL_AUTH_COOKIE_HEADER", "").strip()
        if raw_header:
            return raw_header
        token = os.environ.get("ACTRAIL_AUTH_TOKEN", "").strip()
        if not token:
            return ""
        cookie_name = (
            os.environ.get("ACTRAIL_AUTH_COOKIE_NAME")
            or os.environ.get("ACTRAIL_AUTH_COOKIE")
            or "actrail_auth"
        ).strip()
        return f"{cookie_name}={token}"

    def login(self, password: str) -> None:
        response = self.request("POST", "/api/login", {"password": password}, attach_cookie=False)
        cookie = response.get("_set_cookie", "")
        if not cookie:
            raise ActRailError("login succeeded but no auth cookie was returned")
        self.cookie = cookie

    def request(self, method: str, path: str, body: dict[str, Any] | None = None, attach_cookie: bool = True) -> dict[str, Any]:
        data = None
        headers = {"Accept": "application/json"}
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            headers["Content-Type"] = "application/json"
        if attach_cookie and self.cookie:
            headers["Cookie"] = self.cookie
        req = urllib.request.Request(f"{self.url}{path}", data=data, headers=headers, method=method)
        try:
            with urllib.request.urlopen(req, timeout=20) as resp:
                raw = resp.read().decode("utf-8")
                payload = json.loads(raw) if raw else {}
                set_cookie = cookie_header_from_set_cookie(resp)
                if set_cookie:
                    payload["_set_cookie"] = set_cookie
                return payload
        except urllib.error.HTTPError as exc:
            raw = exc.read().decode("utf-8", errors="replace")
            raise ActRailError(f"{method} {path} failed with HTTP {exc.code}: {raw}") from exc
        except urllib.error.URLError as exc:
            raise ActRailError(f"{method} {path} failed: {exc.reason}") from exc


def print_json(payload: Any) -> None:
    print(json.dumps(payload, indent=2, sort_keys=True))


def cmd_sessions(client: Client, args: argparse.Namespace) -> None:
    query = urllib.parse.urlencode({"limit": args.limit})
    print_json(client.request("GET", f"/api/sessions?{query}"))


def cmd_list(client: Client, args: argparse.Namespace) -> None:
    query = urllib.parse.urlencode({"limit": args.limit})
    payload = client.request("GET", f"/api/scheduler?{query}")
    items = [item for item in payload.get("items", []) if item.get("kind") == "self_reminder"]
    if args.session_id:
        items = [item for item in items if item.get("session_id") == args.session_id]
    if args.state:
        items = [item for item in items if item.get("state") == args.state]
    print_json({"ok": payload.get("ok", True), "self_reminders": items})


def cmd_create(client: Client, args: argparse.Namespace) -> None:
    session_id = args.session_id or os.environ.get("ACTRAIL_SESSION_ID")
    if not session_id:
        raise ActRailError("missing --session-id or ACTRAIL_SESSION_ID")
    duration = args.duration_seconds if args.duration_seconds is not None else args.after
    if duration is None:
        raise ActRailError("missing --after or --duration-seconds")
    payload: dict[str, Any] = {
        "session_id": session_id,
        "duration_seconds": duration,
        "message": args.message,
        "created_by": "agent",
    }
    if args.title:
        payload["title"] = args.title
    print_json(client.request("POST", "/api/scheduler/self-reminders", payload))


def cmd_cancel(client: Client, args: argparse.Namespace) -> None:
    item_id = urllib.parse.quote(args.item_id, safe="")
    print_json(client.request("POST", f"/api/scheduler/self-reminders/{item_id}/cancel", {}))


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Operate ActRail Scheduler self-reminders.")
    parser.add_argument("--base-url", help=f"ActRail base URL. Default: {DEFAULT_BASE_URL}")
    sub = parser.add_subparsers(dest="command", required=True)

    sessions = sub.add_parser("sessions", help="List sessions for target discovery.")
    sessions.add_argument("--limit", type=int, default=100)
    sessions.set_defaults(func=cmd_sessions)

    list_cmd = sub.add_parser("list", help="List Scheduler self-reminders.")
    list_cmd.add_argument("--limit", type=int, default=100)
    list_cmd.add_argument("--session-id")
    list_cmd.add_argument("--state", choices=["scheduled", "delivered", "cancelled", "error", "unsupported"])
    list_cmd.set_defaults(func=cmd_list)

    create = sub.add_parser("create", help="Create a Scheduler self-reminder.")
    create.add_argument("--session-id")
    create.add_argument("--after", type=parse_duration, help="Delay such as 30s, 10m, 2h, or raw seconds.")
    create.add_argument("--duration-seconds", type=int)
    create.add_argument("--title")
    create.add_argument("--message", required=True)
    create.set_defaults(func=cmd_create)

    cancel = sub.add_parser("cancel", help="Cancel a scheduled self-reminder.")
    cancel.add_argument("item_id")
    cancel.set_defaults(func=cmd_cancel)
    return parser


def main(argv: list[str]) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    try:
        client = Client(base_url(args.base_url))
        args.func(client, args)
        return 0
    except (ActRailError, argparse.ArgumentTypeError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
