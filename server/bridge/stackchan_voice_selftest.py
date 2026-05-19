import argparse
import asyncio
import io
import json
import os
import time
from pathlib import Path

import av
import httpx
import websockets

from server.bridge.stackchan_env import load_dotenv


load_dotenv(Path(__file__).resolve().parent)

VOICE_LOCK_ID = os.environ.get("STACKCHAN_VOICE_LOCK_ID", "").strip()
VOICE_INSTRUCT = "小さな妖精みたいなAIの声で、自然で聞き取りやすい日本語で話してください。"


def iter_frames(resampled):
    if resampled is None:
        return []
    return resampled if isinstance(resampled, list) else [resampled]


def wav_to_opus_packets(wav_bytes: bytes, rate: int = 24000, frame_ms: int = 60) -> list[bytes]:
    container = av.open(io.BytesIO(wav_bytes))
    stream = container.streams.audio[0]
    encoder = av.codec.CodecContext.create("libopus", "w")
    encoder.sample_rate = rate
    encoder.layout = "mono"
    encoder.format = "s16"
    encoder.bit_rate = 32000
    encoder.options = {"application": "voip", "frame_duration": str(frame_ms), "vbr": "off"}
    resampler = av.AudioResampler(format="s16", layout="mono", rate=rate)
    packets: list[bytes] = []
    for frame in container.decode(stream):
        for resampled in iter_frames(resampler.resample(frame)):
            for packet in encoder.encode(resampled):
                packets.append(bytes(packet))
    for packet in encoder.encode(None):
        packets.append(bytes(packet))
    return packets


async def synth_input_audio(tts_run_url: str, text: str) -> bytes:
    async with httpx.AsyncClient(timeout=180) as client:
        payload = {
            "text": text,
            "instruct": VOICE_INSTRUCT,
            "seconds": 4.0,
            "num_steps": 20,
        }
        if VOICE_LOCK_ID:
            payload["voice_lock_id"] = VOICE_LOCK_ID
        response = await client.post(
            tts_run_url,
            json=payload,
        )
        response.raise_for_status()
        return response.content


async def run_selftest(bridge_ws_url: str, tts_run_url: str, prompt: str) -> None:
    wav = await synth_input_audio(tts_run_url, prompt)
    packets = wav_to_opus_packets(wav)

    async with websockets.connect(bridge_ws_url, max_size=8_000_000) as ws:
        await ws.send(
            json.dumps(
                {
                    "type": "hello",
                    "version": 1,
                    "transport": "websocket",
                    "audio_params": {
                        "format": "opus",
                        "sample_rate": 16000,
                        "channels": 1,
                        "frame_duration": 60,
                    },
                }
            )
        )
        hello = json.loads(await ws.recv())
        print(json.dumps({"hello": hello}, ensure_ascii=False))

        await ws.send(json.dumps({"session_id": hello["session_id"], "type": "listen", "state": "start", "mode": "auto"}))
        for packet in packets:
            await ws.send(packet)
        await ws.send(json.dumps({"session_id": hello["session_id"], "type": "listen", "state": "stop"}))

        transcript = None
        assistant_text = None
        binary_packets = 0
        first_audio_time = None
        last_audio_time = None
        while True:
            message = await asyncio.wait_for(ws.recv(), timeout=180)
            if isinstance(message, bytes):
                binary_packets += 1
                now = time.monotonic()
                if first_audio_time is None:
                    first_audio_time = now
                last_audio_time = now
                continue
            payload = json.loads(message)
            print(json.dumps(payload, ensure_ascii=False))
            if payload.get("type") == "stt":
                transcript = payload.get("text")
            if payload.get("type") == "tts" and payload.get("state") == "sentence_start":
                assistant_text = payload.get("text")
            if payload.get("type") == "tts" and payload.get("state") == "stop":
                break

        audio_span_sec = 0.0
        if first_audio_time is not None and last_audio_time is not None:
            audio_span_sec = last_audio_time - first_audio_time
        print(json.dumps({"binary_packets": binary_packets, "audio_span_sec": round(audio_span_sec, 2), "transcript": transcript, "assistant_text": assistant_text}, ensure_ascii=False))


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--bridge-ws-url",
        default=os.environ.get("STACKCHAN_SELFTEST_BRIDGE_WS_URL", "ws://127.0.0.1:8787/xiaozhi/ws"),
    )
    parser.add_argument(
        "--tts-run-url",
        default=os.environ.get(
            "STACKCHAN_SELFTEST_TTS_URL",
            os.environ.get("STACKCHAN_TTS_URL", "http://127.0.0.1:8088/api/tts/v1/tts"),
        ),
    )
    parser.add_argument("--prompt", default="こんにちは。調子はどうですか。")
    args = parser.parse_args()
    asyncio.run(run_selftest(args.bridge_ws_url, args.tts_run_url, args.prompt))


if __name__ == "__main__":
    main()
