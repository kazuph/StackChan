#!/usr/bin/env python3
import argparse
import json
import os
import time
import urllib.error
import urllib.request
from pathlib import Path


CONFIG_DIR = Path.home() / ".config" / "stackchan-swiftbar"
ENDPOINT_FILE = CONFIG_DIR / "endpoint.txt"
CAPTURE_LOG_FILE = CONFIG_DIR / "captures.jsonl"
DEFAULT_ENDPOINT = "http://127.0.0.1:8787"
IRREMOTE_API_ENDPOINT = os.environ.get("IRREMOTE_API_ENDPOINT", "https://irremote-worker.kazu-san.workers.dev").rstrip("/")

CARRIER_HZ = 38000
MIN_USEFUL_MCP_RAWLEN = 100

MODE_VALUES = {"auto", "cool", "heat", "dry", "fan"}
FAN_VALUES = {"auto", "silent", "low", "medium", "high", "max"}


def endpoint() -> str:
    value = os.environ.get("STACKCHAN_MCP_ENDPOINT", "").strip()
    if value:
        return value.rstrip("/")
    try:
        value = ENDPOINT_FILE.read_text(encoding="utf-8").strip()
    except FileNotFoundError:
        value = DEFAULT_ENDPOINT
    return value.rstrip("/")


def api_post(path: str, payload: dict, timeout: int = 12) -> dict:
    request = urllib.request.Request(
        f"{IRREMOTE_API_ENDPOINT}{path}",
        data=json.dumps(payload).encode(),
        headers={"content-type": "application/json", "user-agent": "StackChanIRRemote/1.0"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.loads(response.read().decode())
    except urllib.error.HTTPError as exc:
        body = exc.read().decode(errors="replace")
        raise RuntimeError(f"IRremote API HTTP {exc.code}: {body}") from None
    except Exception as exc:
        raise RuntimeError(f"IRremote API error: {exc}") from None


def bridge_post(path: str, payload: dict, timeout: int = 5) -> dict:
    request = urllib.request.Request(
        f"{endpoint()}{path}",
        data=json.dumps(payload).encode(),
        headers={"content-type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.loads(response.read().decode())
    except urllib.error.HTTPError as exc:
        body = exc.read().decode(errors="replace")
        raise RuntimeError(f"HTTP {exc.code}: {body}") from None
    except Exception as exc:
        raise RuntimeError(str(exc)) from None


def raw_string_to_list(raw: str) -> list[int]:
    return [int(value) for value in raw.split(",") if value.strip()]


def raw_list_to_string(raw: list[int]) -> str:
    return ",".join(str(int(value)) for value in raw)


def decoded_description(decoded: dict) -> str:
    if not decoded:
        return ""
    parts: list[str] = []
    if "power" in decoded:
        parts.append(f"Power: {'On' if decoded.get('power') else 'Off'}")
    if decoded.get("mode"):
        parts.append(f"Mode: {decoded['mode']}")
    if decoded.get("temperatureC") is not None:
        parts.append(f"Temp: {decoded['temperatureC']}C")
    if decoded.get("fan"):
        parts.append(f"Fan: {decoded['fan']}")
    if decoded.get("checksumOk") is not None:
        parts.append(f"Checksum: {'OK' if decoded.get('checksumOk') else 'NG'}")
    return ", ".join(parts)


def infer_raw(raw: str, frequency: int = CARRIER_HZ) -> dict:
    values = raw_string_to_list(raw)
    response = api_post("/api/infer", {"raw": values, "frequency": frequency})
    matched = bool(response.get("matched"))
    decoded = response.get("decoded") if isinstance(response.get("decoded"), dict) else {}
    protocol = str(response.get("protocol") or "UNKNOWN")
    manufacturer = str(response.get("manufacturer") or "Unknown")
    return {
        "ok": matched,
        "protocol": protocol,
        "manufacturer": manufacturer,
        "model": response.get("model"),
        "models": response.get("models") or [],
        "bits": len(response.get("state") or []) * 8 if response.get("state") else None,
        "state": response.get("state") or [],
        "description": decoded_description(decoded),
        "decoded": decoded,
        "confidence": response.get("confidence", 0),
        "candidates": response.get("candidates") or [],
        "supported_send": matched,
        "source": "irremote-worker",
    }


def iter_captures(limit: int) -> list[dict]:
    try:
        lines = CAPTURE_LOG_FILE.read_text(encoding="utf-8").splitlines()
    except FileNotFoundError:
        return []

    captures: list[dict] = []
    for line in reversed(lines):
        if len(captures) >= limit:
            break
        try:
            row = json.loads(line)
        except json.JSONDecodeError:
            continue
        captures.append(row)
    return captures


def decode_latest(limit: int = 80, min_durations: int = 100, max_age: float = 30.0, after_timestamp: float = 0.0) -> None:
    skipped_short = 0
    newest_age = None
    errors: list[str] = []
    for row in iter_captures(limit):
        timestamp = float(row.get("timestamp") or 0)
        if after_timestamp and timestamp <= after_timestamp:
            break
        age_sec = round(time.time() - timestamp, 1)
        newest_age = age_sec if newest_age is None else newest_age
        if age_sec > max_age:
            break
        durations = int(row.get("durations") or 0)
        raw = str(row.get("raw_usec") or "").strip()
        if durations < min_durations or not raw:
            skipped_short += 1
            continue
        try:
            decoded = infer_raw(raw)
        except Exception as exc:
            errors.append(f"durations={durations}, age_sec={age_sec}, error={exc}")
            continue
        if not decoded["ok"]:
            errors.append(f"durations={durations}, age_sec={age_sec}, error=IRremote API did not match")
            continue
        decoded.update(
            {
                "ok": True,
                "timestamp": row.get("timestamp"),
                "age_sec": age_sec,
                "captured_durations": durations,
                "supported_send": True,
            }
        )
        print(json.dumps(decoded, ensure_ascii=False))
        return
    if newest_age is None:
        raise RuntimeError("no IR captures yet")
    detail = errors[-1] if errors else f"skipped_short={skipped_short}"
    raise RuntimeError(f"no decodable AC frame in the last {max_age:g}s: newest_age_sec={newest_age}, {detail}")


def call_tool(name: str, arguments: dict, timeout: int = 12) -> dict:
    payload = json.dumps({"name": name, "arguments": arguments, "timeout": timeout}).encode()
    request = urllib.request.Request(
        f"{endpoint()}/mcp/call",
        data=payload,
        headers={"content-type": "application/json"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout + 3) as response:
            return json.loads(response.read().decode())
    except urllib.error.HTTPError as exc:
        body = exc.read().decode(errors="replace")
        raise RuntimeError(f"HTTP {exc.code}: {body}") from None
    except Exception as exc:
        raise RuntimeError(str(exc)) from None


def ensure_ok(result: dict) -> None:
    if result.get("ok") is False:
        raise RuntimeError(json.dumps(result, ensure_ascii=False))


def mcp_text(result: dict) -> str:
    content = result.get("result", {}).get("content", [])
    if not content:
        raise RuntimeError(json.dumps(result, ensure_ascii=False))
    first = content[0]
    if first.get("type") != "text":
        raise RuntimeError(json.dumps(result, ensure_ascii=False))
    return str(first.get("text") or "")


def status() -> None:
    result = call_tool("self.get_device_status", {}, 5)
    ensure_ok(result)
    print(json.dumps(result, ensure_ascii=False))


def speak(text: str) -> None:
    result = bridge_post("/speak", {"text": text}, 5)
    print(json.dumps(result, ensure_ascii=False))


def announce_ir(payload_json: str) -> None:
    try:
        payload = json.loads(payload_json)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"invalid IR payload JSON: {exc}") from None
    if not isinstance(payload, dict):
        raise RuntimeError("IR payload must be a JSON object")
    result = bridge_post("/ir/decode-speech", payload, 5)
    print(json.dumps(result, ensure_ascii=False))


def reset_receiver() -> None:
    result = call_tool("self.robot.reset_ir_receiver", {}, 5)
    ensure_ok(result)
    print(json.dumps({"ok": True, "reset": "ir_receiver"}, ensure_ascii=False))


def decoded_from_mcp_irremote(latest: dict, scanned: int = 1) -> dict:
    frame_count = int(latest.get("frame_count") or latest.get("decode_count") or 0)
    age_sec = round(int(latest.get("age_ms") or 0) / 1000, 1)
    raw = str(latest.get("raw_usec") or "")
    captured_durations = int(latest.get("durations") or latest.get("rawlen") or 0)
    if captured_durations and captured_durations < MIN_USEFUL_MCP_RAWLEN:
        return {
            "ok": False,
            "ignored": True,
            "short_frame": True,
            "protocol": "UNKNOWN",
            "manufacturer": "Unknown",
            "bits": None,
            "state_hex": str(latest.get("code") or ""),
            "description": "",
            "frame_count": frame_count,
            "age_sec": age_sec,
            "captured_durations": captured_durations,
            "raw_usec": raw,
            "scanned_frames": scanned,
            "supported_send": False,
            "source": "mcp-irremoteesp8266",
            "error": f"short non-AC frame ignored: durations={captured_durations}",
        }
    if not raw:
        raise RuntimeError("latest IR frame has no raw_usec")
    decoded = infer_raw(raw)
    decoded.update(
        {
            "frame_count": frame_count,
            "age_sec": age_sec,
            "captured_durations": captured_durations,
            "raw_usec": raw,
            "scanned_frames": scanned,
        }
    )
    if not decoded["ok"]:
        decoded["ignored"] = True
        decoded["error"] = "IRremote Web API did not match this frame"
    return decoded


def decode_mcp_latest(min_durations: int = 100, max_age: float = 30.0, after_frame_count: int = 0) -> None:
    result = call_tool("self.robot.get_ir_rx_latest", {}, 5)
    ensure_ok(result)
    latest = json.loads(mcp_text(result))
    frame_count = int(latest.get("frame_count") or latest.get("decode_count") or 0)
    if frame_count <= 0:
        raise RuntimeError("no IR captures yet")
    if after_frame_count and frame_count <= after_frame_count:
        raise RuntimeError("no newer IR frame yet")
    print(json.dumps(decoded_from_mcp_irremote(latest), ensure_ascii=False))


def get_mcp_ir_status() -> dict:
    result = call_tool("self.robot.get_ir_rx_status", {}, 4)
    ensure_ok(result)
    return json.loads(mcp_text(result))


def decode_mcp_payload(latest: dict, min_durations: int, max_age: float, after_frame_count: int) -> dict:
    frame_count = int(latest.get("frame_count") or latest.get("decode_count") or 0)
    if frame_count <= 0:
        raise RuntimeError("no IR captures yet")
    if after_frame_count and frame_count <= after_frame_count:
        raise RuntimeError("no newer IR frame yet")
    return decoded_from_mcp_irremote(latest)


def watch_mcp_ir(interval: float = 0.4, min_durations: int = 100, max_age: float = 180.0) -> None:
    try:
        initial_status = get_mcp_ir_status()
        last_frame_count = int(initial_status.get("frame_count") or 0)
    except Exception:
        last_frame_count = 0
    last_heartbeat = 0.0
    while True:
        try:
            status_row = get_mcp_ir_status()
            frame_count = int(status_row.get("frame_count") or 0)
            now = time.time()
            if now - last_heartbeat >= 10:
                print(
                    json.dumps(
                        {
                            "event": "heartbeat",
                            "frame_count": frame_count,
                            "receiver_configured": status_row.get("receiver_configured"),
                            "queue_drop_count": status_row.get("queue_drop_count"),
                            "overflow_frame_count": status_row.get("overflow_frame_count"),
                        },
                        ensure_ascii=False,
                    ),
                    flush=True,
                )
                last_heartbeat = now
            if frame_count > last_frame_count:
                result = call_tool("self.robot.get_ir_rx_latest", {}, 5)
                ensure_ok(result)
                latest = json.loads(mcp_text(result))
                payload = decode_mcp_payload(latest, min_durations, max_age, last_frame_count)
                print(json.dumps(payload, ensure_ascii=False), flush=True)
                last_frame_count = frame_count
            time.sleep(interval)
        except KeyboardInterrupt:
            raise
        except Exception as exc:
            print(json.dumps({"event": "error", "error": str(exc)}, ensure_ascii=False), flush=True)
            time.sleep(max(1.0, interval))


def send_raw_timings(raw: str, timeout: int = 15, carrier_hz: int = CARRIER_HZ) -> dict:
    result = call_tool(
        "self.robot.send_ir_raw",
        {"timings_usec": raw, "carrier_hz": carrier_hz},
        timeout,
    )
    ensure_ok(result)
    return result


def api_fan_value(fan: str) -> str:
    return "low" if fan == "silent" else fan


def send(power: bool, mode: str, temp: int, fan: str, protocol: str = "") -> None:
    if not protocol:
        raise RuntimeError("protocol is required for IR generation")
    payload = {
        "protocol": protocol,
        "power": power,
        "mode": mode,
        "temperatureC": temp,
        "fan": api_fan_value(fan),
    }
    generated = api_post("/api/generate", payload)
    raw = generated.get("raw")
    if not isinstance(raw, list) or not raw:
        raise RuntimeError(f"IRremote API returned no raw timings: {json.dumps(generated, ensure_ascii=False)}")
    carrier_hz = int(generated.get("frequency") or CARRIER_HZ)
    send_raw_timings(raw_list_to_string(raw), timeout=20, carrier_hz=carrier_hz)
    print(
        json.dumps(
            {
                "ok": True,
                "protocol": generated.get("protocol") or protocol,
                "manufacturer": generated.get("manufacturer"),
                "model": generated.get("model"),
                "mode": "generated_by_irremote_web_api",
                "frequency": carrier_hz,
                "durations": len(raw),
                "settings": generated.get("settings") or payload,
            },
            ensure_ascii=False,
        )
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)

    send_parser = subparsers.add_parser("send")
    send_parser.add_argument("--protocol", default="")
    send_parser.add_argument("--power", choices=["on", "off"], required=True)
    send_parser.add_argument("--mode", choices=sorted(MODE_VALUES), default="cool")
    send_parser.add_argument("--temp", type=int, default=26)
    send_parser.add_argument("--fan", choices=sorted(FAN_VALUES), default="auto")

    subparsers.add_parser("status")
    speak_parser = subparsers.add_parser("speak")
    speak_parser.add_argument("--text", required=True)
    announce_parser = subparsers.add_parser("announce-ir")
    announce_parser.add_argument("--payload", required=True)
    subparsers.add_parser("reset-receiver")
    decode_parser = subparsers.add_parser("decode-latest")
    decode_parser.add_argument("--limit", type=int, default=80)
    decode_parser.add_argument("--min-durations", type=int, default=100)
    decode_parser.add_argument("--max-age", type=float, default=30.0)
    decode_parser.add_argument("--after-timestamp", type=float, default=0.0)
    decode_mcp_parser = subparsers.add_parser("decode-mcp-latest")
    decode_mcp_parser.add_argument("--min-durations", type=int, default=100)
    decode_mcp_parser.add_argument("--max-age", type=float, default=30.0)
    decode_mcp_parser.add_argument("--after-frame-count", type=int, default=0)
    watch_parser = subparsers.add_parser("watch-mcp-ir")
    watch_parser.add_argument("--interval", type=float, default=0.4)
    watch_parser.add_argument("--min-durations", type=int, default=100)
    watch_parser.add_argument("--max-age", type=float, default=180.0)

    args = parser.parse_args()
    if args.command == "status":
        status()
    elif args.command == "speak":
        speak(args.text)
    elif args.command == "announce-ir":
        announce_ir(args.payload)
    elif args.command == "reset-receiver":
        reset_receiver()
    elif args.command == "decode-latest":
        decode_latest(args.limit, args.min_durations, args.max_age, args.after_timestamp)
    elif args.command == "decode-mcp-latest":
        decode_mcp_latest(args.min_durations, args.max_age, args.after_frame_count)
    elif args.command == "watch-mcp-ir":
        watch_mcp_ir(args.interval, args.min_durations, args.max_age)
    else:
        send(args.power == "on", args.mode, args.temp, args.fan, args.protocol)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except RuntimeError as exc:
        print(f"ERROR: {exc}")
        raise SystemExit(1)
