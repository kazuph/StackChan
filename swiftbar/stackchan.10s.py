#!/usr/bin/env python3
import json
import os
import sys
import urllib.error
import urllib.request
import time
from pathlib import Path
from typing import Optional

CONFIG_DIR = Path.home() / ".config" / "stackchan-swiftbar"
ENDPOINT_FILE = CONFIG_DIR / "endpoint.txt"
LATEST_IR_FILE = CONFIG_DIR / "latest_ir.json"
ACTION_LOG_FILE = CONFIG_DIR / "action.log"
SNIFFER_PID_FILE = CONFIG_DIR / "sniffer.pid"
DEFAULT_ENDPOINT = "http://192.168.11.12:8787"

SCRIPT_DIR = Path(__file__).resolve().parent
ACTION = SCRIPT_DIR / "stackchan_action.py"
ACTION_SH = SCRIPT_DIR / "stackchan_action.sh"
SNIFFER = SCRIPT_DIR / "stackchan_ir_sniffer.py"


def read_endpoint() -> str:
    endpoint = os.environ.get("STACKCHAN_MCP_ENDPOINT", "").strip()
    if endpoint:
        return endpoint.rstrip("/")
    try:
        endpoint = ENDPOINT_FILE.read_text(encoding="utf-8").strip()
    except FileNotFoundError:
        endpoint = DEFAULT_ENDPOINT
    return endpoint.rstrip("/")


def post_json(path: str, payload: dict, timeout: float = 2.0) -> dict:
    data = json.dumps(payload).encode("utf-8")
    request = urllib.request.Request(
        f"{read_endpoint()}{path}",
        data=data,
        headers={"content-type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return json.loads(response.read().decode("utf-8"))


def call_tool(name: str, arguments: Optional[dict] = None, timeout: float = 2.0) -> dict:
    return post_json("/mcp/call", {"name": name, "arguments": arguments or {}, "timeout": int(timeout)}, timeout + 1)


def is_tool_available(tool_name: str) -> bool:
    try:
        tools = post_json("/mcp/list", {}, 2.0)["result"]["tools"]
    except Exception:
        return False
    return any(tool.get("name") == tool_name for tool in tools)


def get_status_label() -> str:
    try:
        result = call_tool("self.get_device_status", {}, 2.0)
    except urllib.error.HTTPError as exc:
        if exc.code == 409:
            return "StackChan: waiting for device session"
        return f"StackChan: bridge error {exc.code}"
    except Exception:
        return "StackChan: offline"
    if result.get("result", {}).get("isError"):
        return "StackChan: error"
    return "StackChan: online"


def menu_item(title: str, *args: str, refresh: bool = False) -> None:
    params = [f'bash="{ACTION_SH}"', "terminal=false", f"refresh={'true' if refresh else 'false'}"]
    for index, value in enumerate(args, 1):
        escaped = value.replace('"', '\\"')
        params.append(f'param{index}="{escaped}"')
    print(f"{title} | {' '.join(params)}")


def read_latest_ir() -> Optional[dict]:
    try:
        return json.loads(LATEST_IR_FILE.read_text(encoding="utf-8"))
    except (FileNotFoundError, json.JSONDecodeError):
        return None


def read_last_action() -> str:
    try:
        lines = ACTION_LOG_FILE.read_text(encoding="utf-8").strip().splitlines()
    except FileNotFoundError:
        return ""
    return lines[-1] if lines else ""


def sniffer_status() -> str:
    try:
        pid = int(SNIFFER_PID_FILE.read_text(encoding="utf-8").strip())
        os.kill(pid, 0)
        return f"IR RX monitor: running pid={pid}"
    except Exception:
        return "IR RX monitor: stopped"


def compact_raw(raw: str, limit: int = 82) -> str:
    if len(raw) <= limit:
        return raw
    return raw[:limit - 1] + "..."


def analyze_hitachi(raw: str) -> str:
    try:
        values = [int(value) for value in raw.split(",") if value]
    except ValueError:
        return "Analysis: invalid raw"
    if len(values) < 6:
        return "Analysis: too short"
    leader_ok = abs(values[0] - 29784) < 3500 and abs(values[1] - 49290) < 7000
    header_ok = abs(values[2] - 3416) < 700 and abs(values[3] - 1604) < 500
    pairs = list(zip(values[4::2], values[5::2]))
    decodable = [
        (mark, space)
        for mark, space in pairs
        if 180 <= mark <= 750 and 200 <= space <= 1700
    ]
    bits = len(decodable)
    proto = "HITACHI_AC424-like" if leader_ok and header_ok else "unknown"
    expected = "424 bits expected" if proto == "HITACHI_AC424-like" else "no library match"
    return f"Analysis: {proto}, {bits} bits-ish, {expected}"


def print_ir_receive_menu() -> None:
    latest = read_latest_ir()
    menu_item("IR RX: start monitor", "ir-sniff-start")
    menu_item("IR RX: stop monitor", "ir-sniff-stop")
    if not latest:
        print("Latest IR: none")
        return

    age = max(0, int(time.time() - float(latest.get("timestamp", 0))))
    count = latest.get("durations", "?")
    raw = str(latest.get("raw_usec", ""))
    print(f"Latest IR: {count} durations, {age}s ago")
    print(analyze_hitachi(raw))
    print(f"Raw: {compact_raw(raw)}")
    menu_item("IR RX: copy latest raw", "ir-copy-latest")
    menu_item("HITACHI: resend latest raw", "ir-resend-latest")


def main() -> int:
    endpoint = read_endpoint()
    online = get_status_label()
    print("🤖" if online.endswith("online") else "StackChan")
    print("---")
    status_color = "green" if online.endswith("online") else ("orange" if "waiting" in online else "red")
    print(f"{online} | color={status_color}")
    print(f"Endpoint: {endpoint}")
    last_action = read_last_action()
    if last_action:
        print(f"Last action: {compact_raw(last_action, 96)}")
    print(sniffer_status())
    print("---")
    print_ir_receive_menu()
    print("---")
    menu_item("Reconnect / recover", "reconnect", refresh=True)
    print("---")
    menu_item("HITACHI: Cool ON 24C", "hitachi-cool-on")
    menu_item("HITACHI: OFF", "hitachi-off")
    print("---")
    menu_item("IR: blink LED 10x", "ir-blink")
    menu_item("IR: NEC test send", "ir-nec")
    print("---")
    print("Refresh | refresh=true")
    print(f"Config dir | href=file://{CONFIG_DIR}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
