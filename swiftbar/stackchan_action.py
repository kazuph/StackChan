#!/usr/bin/env python3
import json
import os
import signal
import sys
import time
import urllib.request
import urllib.error
import subprocess
import fcntl
from pathlib import Path
from typing import Optional

CONFIG_DIR = Path.home() / ".config" / "stackchan-swiftbar"
ENDPOINT_FILE = CONFIG_DIR / "endpoint.txt"
LATEST_IR_FILE = CONFIG_DIR / "latest_ir.json"
SNIFFER_PID_FILE = CONFIG_DIR / "sniffer.pid"
SNIFFER_HEALTH_FILE = CONFIG_DIR / "sniffer.health.json"
ACTION_LOG_FILE = CONFIG_DIR / "action.log"
LOCK_FILE = CONFIG_DIR / "action.lock"
DEFAULT_ENDPOINT = "http://192.168.11.12:8787"
SCRIPT_DIR = Path(__file__).resolve().parent
SNIFFER = SCRIPT_DIR / "stackchan_ir_sniffer.py"

def endpoint() -> str:
    value = os.environ.get("STACKCHAN_MCP_ENDPOINT", "").strip()
    if not value:
        try:
            value = ENDPOINT_FILE.read_text(encoding="utf-8").strip()
        except FileNotFoundError:
            value = DEFAULT_ENDPOINT
    return value.rstrip("/")


def call_tool(name: str, arguments: Optional[dict] = None, timeout: int = 8) -> dict:
    payload = {"name": name, "arguments": arguments or {}, "timeout": timeout}
    request = urllib.request.Request(
        f"{endpoint()}/mcp/call",
        data=json.dumps(payload).encode("utf-8"),
        headers={"content-type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout + 2) as response:
            return json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {exc.code}: {body}") from exc


def post_bridge(path: str, payload: dict, timeout: int = 5) -> dict:
    request = urllib.request.Request(
        f"{endpoint()}{path}",
        data=json.dumps(payload).encode("utf-8"),
        headers={"content-type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.loads(response.read().decode("utf-8"))
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {exc.code}: {body}") from exc


def ensure_ok(result: dict) -> None:
    if result.get("result", {}).get("isError"):
        raise RuntimeError(json.dumps(result, ensure_ascii=False))
    content = result.get("result", {}).get("content", [])
    if content and content[0].get("type") == "text" and content[0].get("text") == "false":
        raise RuntimeError(json.dumps(result, ensure_ascii=False))


def log_action(message: str) -> None:
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    with ACTION_LOG_FILE.open("a", encoding="utf-8") as file:
        file.write(f"{time.strftime('%Y-%m-%d %H:%M:%S')} {message}\n")


def speak(text: str) -> None:
    try:
        post_bridge("/speak", {"text": text}, 5)
    except Exception as exc:
        log_action(f"speak-error: {exc}")


def set_indicator(red: int, green: int, blue: int) -> None:
    try:
        ensure_ok(call_tool("self.robot.set_led_color", {"red": red, "green": green, "blue": blue}, 5))
    except Exception as exc:
        log_action(f"indicator-error: {exc}")


def mark_ir_started(text: Optional[str] = None) -> None:
    set_indicator(168, 120, 0)
    if text:
        speak(text)


def mark_ir_result(success: bool) -> None:
    if success:
        set_indicator(0, 168, 0)
    else:
        set_indicator(168, 0, 0)
    time.sleep(0.8)
    set_indicator(0, 0, 0)


def read_latest_ir_raw() -> str:
    data = json.loads(LATEST_IR_FILE.read_text(encoding="utf-8"))
    raw = str(data.get("raw_usec", "")).strip()
    if not raw:
        raise RuntimeError("latest IR raw is empty")
    count = len(raw.split(","))
    if count > 1200:
        raise RuntimeError(f"latest IR raw is too long for firmware sendIrRaw: {count} durations")
    return raw


def start_sniffer() -> None:
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    if SNIFFER_PID_FILE.exists():
        try:
            pid = int(SNIFFER_PID_FILE.read_text(encoding="utf-8").strip())
            os.kill(pid, 0)
            command = Path(f"/proc/{pid}/cmdline")
            if sys.platform.startswith("linux") and command.exists():
                if str(SNIFFER) in command.read_text(encoding="utf-8", errors="replace"):
                    return
            else:
                completed = subprocess.run(
                    ["/bin/ps", "-p", str(pid), "-o", "command="],
                    check=False,
                    text=True,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.DEVNULL,
                )
                if str(SNIFFER) in completed.stdout:
                    if sniffer_health_age() <= 8:
                        return
                    log_action(f"sniffer stale health: pid={pid} age={sniffer_health_age():.1f}s")
                    os.kill(pid, signal.SIGTERM)
                    time.sleep(0.3)
            log_action(f"sniffer stale pid reused by another process: {pid}")
        except Exception:
            pass
        SNIFFER_PID_FILE.unlink(missing_ok=True)

    process = subprocess.Popen(
        ["/usr/bin/python3", str(SNIFFER)],
        stdin=subprocess.DEVNULL,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.DEVNULL,
        start_new_session=True,
    )
    SNIFFER_PID_FILE.write_text(str(process.pid), encoding="utf-8")


def stop_sniffer() -> None:
    try:
        pid = int(SNIFFER_PID_FILE.read_text(encoding="utf-8").strip())
    except Exception:
        return
    try:
        os.kill(pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    SNIFFER_PID_FILE.unlink(missing_ok=True)


def is_sniffer_running() -> bool:
    try:
        pid = int(SNIFFER_PID_FILE.read_text(encoding="utf-8").strip())
        os.kill(pid, 0)
        return True
    except Exception:
        return False


def sniffer_health_age() -> float:
    try:
        data = json.loads(SNIFFER_HEALTH_FILE.read_text(encoding="utf-8"))
        return time.time() - float(data.get("timestamp") or 0)
    except Exception:
        return float("inf")


def sniffer_status() -> dict:
    running = is_sniffer_running()
    try:
        health = json.loads(SNIFFER_HEALTH_FILE.read_text(encoding="utf-8"))
    except Exception:
        health = {}
    health_age = sniffer_health_age()
    return {
        "running": running,
        "pid": SNIFFER_PID_FILE.read_text(encoding="utf-8").strip() if SNIFFER_PID_FILE.exists() else "",
        "health_age_sec": None if health_age == float("inf") else round(health_age, 1),
        "health": health,
    }


def send_raw_once(raw: str) -> None:
    start_sniffer()
    success = False
    try:
        ensure_ok(call_tool("self.robot.send_ir_raw", {"timings_usec": raw, "carrier_hz": 38000}, 10))
        success = True
    finally:
        mark_ir_result(success)


def reconnect() -> None:
    try:
        ensure_ok(call_tool("self.get_device_status", {}, 5))
        set_indicator(0, 0, 0)
    except Exception as exc:
        log_action(f"reconnect-wait-device-session: {exc}")
    start_sniffer()


def main(argv: list[str]) -> int:
    if len(argv) < 2:
        raise SystemExit("usage: stackchan_action.py <action> [args...]")

    action = argv[1]
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    lock_file = LOCK_FILE.open("w", encoding="utf-8")
    try:
        fcntl.flock(lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        log_action(f"busy {action}")
        return 0
    log_action(f"start {action}")
    try:
        if action == "ir-nec":
            for _ in range(5):
                ensure_ok(call_tool("self.robot.send_ir_nec_test", {"address": 0, "command": 85}, 5))
                time.sleep(0.35)
        elif action == "ir-blink":
            ensure_ok(call_tool("self.robot.test_ir_gpio_blink", {"active_low": False, "pulses": 10}, 8))
        elif action == "ir-resend-latest":
            latest = read_latest_ir()
            if not latest:
                raise RuntimeError("no latest IR capture")
            send_raw_once(str(latest.get("raw_usec", "")))
        elif action == "reconnect":
            reconnect()
        elif action == "ir-sniff-start":
            start_sniffer()
        elif action == "ir-sniff-status":
            start_sniffer()
            print(json.dumps(sniffer_status(), ensure_ascii=False))
        elif action == "ir-sniff-stop":
            stop_sniffer()
        elif action == "ir-copy-latest":
            raw = read_latest_ir_raw()
            subprocess.run(["/usr/bin/pbcopy"], input=raw.encode("utf-8"), check=True)
        elif action == "led-off":
            ensure_ok(call_tool("self.robot.set_led_color", {"red": 0, "green": 0, "blue": 0}, 5))
        elif action == "led":
            red, green, blue = (int(argv[2]), int(argv[3]), int(argv[4]))
            ensure_ok(call_tool("self.robot.set_led_color", {"red": red, "green": green, "blue": blue}, 5))
        elif action == "head":
            yaw, pitch = (int(argv[2]), int(argv[3]))
            ensure_ok(call_tool("self.robot.set_head_angles", {"yaw": yaw, "pitch": pitch, "speed": 250}, 5))
        else:
            raise SystemExit(f"unknown action: {action}")
    except Exception as exc:
        log_action(f"error {action}: {exc}")
        return 1
    log_action(f"done {action}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
