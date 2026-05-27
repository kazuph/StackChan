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
ACTION_LOG_FILE = CONFIG_DIR / "action.log"
LOCK_FILE = CONFIG_DIR / "action.lock"
DEFAULT_ENDPOINT = "http://192.168.11.12:8787"
SCRIPT_DIR = Path(__file__).resolve().parent
SNIFFER = SCRIPT_DIR / "stackchan_ir_sniffer.py"

HITACHI_AC424_HDR_MARK = 3416
HITACHI_AC424_HDR_SPACE = 1604
HITACHI_AC424_BIT_MARK = 463
HITACHI_AC424_ONE_SPACE = 1208
HITACHI_AC424_ZERO_SPACE = 372
HITACHI_AC424_MIN_GAP = 100000
HITACHI_AC424_LDR_MARK = 29784
HITACHI_AC424_LDR_SPACE = 49290
HITACHI_AC424_BUTTON_BYTE = 11
HITACHI_AC424_BUTTON_POWER = 0x13
HITACHI_AC424_TEMP_BYTE = 13
HITACHI_AC424_MODE_BYTE = 25
HITACHI_AC424_POWER_BYTE = 27
HITACHI_AC424_MODE_COOL = 3
HITACHI_AC424_FAN_AUTO = 5
HITACHI_AC424_POWER_ON = 0xF1
HITACHI_AC424_POWER_OFF = 0xE1

HITACHI_AC424_BASE = [
    0x01, 0x10, 0x00, 0x40, 0xBF, 0xFF, 0x00, 0xCC, 0x33, 0x92, 0x6D, 0x13, 0xEC,
    0x5C, 0xA3, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0xFF, 0x53,
    0xAC, 0xF1, 0x0E, 0x00, 0xFF, 0x00, 0xFF, 0x80, 0x7F, 0x03, 0xFC, 0x01, 0xFE,
    0x88, 0x77, 0x00, 0xFF, 0x00, 0xFF, 0xFF, 0x00, 0xFF, 0x00, 0xFF, 0x00, 0xFF,
    0x00,
]

HITACHI_RAW = (
    "29914,49798,3374,1684,410,1270,410,430,411,429,411,430,410,430,409,430,411,429,411,430,"
    "410,430,410,430,409,430,410,430,410,1270,410,430,410,430,410,430,410,429,411,429,411,429,"
    "411,429,411,429,410,430,411,429,410,429,411,429,411,429,411,429,412,427,412,428,412,429,"
    "411,1268,412,428,412,1268,412,1266,414,1266,413,1265,413,1267,414,1265,412,426,414,1266,"
    "413,1267,413,1266,413,1267,413,1266,413,1266,413,1267,412,1267,413,1266,412,427,413,427,"
    "413,426,414,426,414,427,413,426,414,426,413,426,414,425,414,426,413,1266,413,1266,414,426,"
    "413,427,414,1266,414,1265,414,1266,414,1266,414,426,413,427,414,1266,413,1267,413,427,"
    "413,427,413,426,413,1266,413,427,415,425,413,1267,412,427,413,427,413,1266,413,1266,"
    "413,427,413,1266,413,1266,412,428,412,1267,416,1263,412,427,412,1267,412,1267,412,428,"
    "412,428,411,1268,411,428,412,428,411,429,411,429,411,429,410,1269,411,1268,411,429,"
    "410,1270,410,1269,410,1270,409,431,408,433,407,433,407,434,406,1273,405,1275,404,437,"
    "403,1276,403,1301,378,1302,377,1302,377,1303,376,464,376,465,349,1330,349,492,348,517,"
    "323,517,322,518,322,519,320,521,319,546,293,573,266,1387,293,1386,293,1413,213,1466,138"
)


def set_bits(state: list[int], index: int, offset: int, size: int, value: int) -> None:
    mask = ((1 << size) - 1) << offset
    state[index] = (state[index] & ~mask) | ((value << offset) & mask)


def invert_byte_pairs(state: list[int], start: int = 3) -> None:
    for index in range(start + 1, len(state), 2):
        state[index] = (~state[index - 1]) & 0xFF


def build_hitachi_ac424_raw(power_on: bool, temp: int = 24) -> str:
    state = HITACHI_AC424_BASE[:]
    state[HITACHI_AC424_BUTTON_BYTE] = HITACHI_AC424_BUTTON_POWER
    set_bits(state, HITACHI_AC424_TEMP_BYTE, 2, 6, max(16, min(32, temp)))
    set_bits(state, HITACHI_AC424_MODE_BYTE, 0, 4, HITACHI_AC424_MODE_COOL)
    set_bits(state, HITACHI_AC424_MODE_BYTE, 4, 4, HITACHI_AC424_FAN_AUTO)
    state[HITACHI_AC424_POWER_BYTE] = HITACHI_AC424_POWER_ON if power_on else HITACHI_AC424_POWER_OFF
    invert_byte_pairs(state)

    timings = [HITACHI_AC424_LDR_MARK, HITACHI_AC424_LDR_SPACE, HITACHI_AC424_HDR_MARK, HITACHI_AC424_HDR_SPACE]
    for byte in state:
        for bit_index in range(8):
            timings.append(HITACHI_AC424_BIT_MARK)
            timings.append(HITACHI_AC424_ONE_SPACE if byte & (1 << bit_index) else HITACHI_AC424_ZERO_SPACE)
    timings.extend([HITACHI_AC424_BIT_MARK, HITACHI_AC424_MIN_GAP])
    return ",".join(str(value) for value in timings)


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
            return
        except Exception:
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


def send_raw_once(raw: str) -> None:
    was_sniffing = is_sniffer_running()
    if was_sniffing:
        stop_sniffer()
        time.sleep(0.5)
    success = False
    try:
        ensure_ok(call_tool("self.robot.send_ir_raw", {"timings_usec": raw, "carrier_hz": 38000}, 10))
        success = True
    finally:
        mark_ir_result(success)
        if was_sniffing:
            start_sniffer()


def reconnect() -> None:
    stop_sniffer()
    time.sleep(0.5)
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
        elif action == "ac-hitachi-raw":
            send_raw_once(HITACHI_RAW)
        elif action == "hitachi-cool-on":
            mark_ir_started("冷房をつけるね。")
            send_raw_once(build_hitachi_ac424_raw(True, 24))
        elif action == "hitachi-off":
            mark_ir_started("エアコンを消すね。")
            send_raw_once(build_hitachi_ac424_raw(False, 24))
        elif action == "reconnect":
            reconnect()
        elif action == "ir-sniff-start":
            start_sniffer()
        elif action == "ir-sniff-stop":
            stop_sniffer()
        elif action == "ir-copy-latest":
            raw = read_latest_ir_raw()
            subprocess.run(["/usr/bin/pbcopy"], input=raw.encode("utf-8"), check=True)
        elif action == "ir-resend-latest":
            raw = read_latest_ir_raw()
            send_raw_once(raw)
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
