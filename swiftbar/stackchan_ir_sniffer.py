#!/usr/bin/env python3
import json
import re
import signal
import sys
import time
from pathlib import Path

import serial
from serial.tools import list_ports

CONFIG_DIR = Path.home() / ".config" / "stackchan-swiftbar"
LATEST_IR_FILE = CONFIG_DIR / "latest_ir.json"
CAPTURE_LOG_FILE = CONFIG_DIR / "captures.jsonl"
LOG_FILE = CONFIG_DIR / "ir_sniffer.log"
PORT_FILE = CONFIG_DIR / "serial_port.txt"
BAUDRATE = 115200

IR_RE = re.compile(r"IR-SNIFF durations=(?P<count>\d+).*?raw_usec=(?P<raw>[0-9,]+)")
running = True


def stop(_signum, _frame):
    global running
    running = False


def log(message: str) -> None:
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    with LOG_FILE.open("a", encoding="utf-8") as file:
        file.write(f"{time.strftime('%Y-%m-%d %H:%M:%S')} {message}\n")


def find_port() -> str:
    try:
        configured = PORT_FILE.read_text(encoding="utf-8").strip()
    except FileNotFoundError:
        configured = ""
    if configured:
        return configured

    ports = [port.device for port in list_ports.comports()]
    for prefix in ("/dev/cu.usbmodem", "/dev/tty.usbmodem"):
        for port in ports:
            if port.startswith(prefix):
                return port
    raise RuntimeError(f"StackChan serial port not found: {ports}")


def save_frame(count: int, raw: str) -> None:
    values = [int(value) for value in raw.split(",") if value]
    if count > 1200 or not values or values[0] < 20000:
        log(f"ignored durations={count} first={values[0] if values else '?'} bytes={len(raw)}")
        return
    payload = {
        "timestamp": time.time(),
        "durations": count,
        "raw_usec": raw,
    }
    tmp = LATEST_IR_FILE.with_suffix(".json.tmp")
    tmp.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")
    tmp.replace(LATEST_IR_FILE)
    with CAPTURE_LOG_FILE.open("a", encoding="utf-8") as file:
        file.write(json.dumps(payload, ensure_ascii=False) + "\n")


def main() -> int:
    signal.signal(signal.SIGTERM, stop)
    signal.signal(signal.SIGINT, stop)
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)

    while running:
        try:
            port = find_port()
            log(f"opening {port}")
            with serial.Serial(port, BAUDRATE, timeout=1) as ser:
                while running:
                    line = ser.readline().decode("utf-8", errors="replace").strip()
                    if not line:
                        continue
                    match = IR_RE.search(line)
                    if not match:
                        continue
                    count = int(match.group("count"))
                    raw = match.group("raw")
                    save_frame(count, raw)
                    log(f"captured durations={count} bytes={len(raw)}")
        except Exception as exc:
            log(f"error: {exc}")
            time.sleep(2)
    log("stopped")
    return 0


if __name__ == "__main__":
    sys.exit(main())
