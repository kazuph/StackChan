import io
import os
import wave
from pathlib import Path

import httpx
import pytest

from server.bridge.stackchan_env import load_dotenv
from server.bridge.stackchan_voice_bridge import VOICE_INSTRUCT, sanitize_llm_text


load_dotenv(Path(__file__).resolve().parents[1])

REMOTE_STT_URL = os.environ["STACKCHAN_STT_URL"]
REMOTE_TTS_URL = os.environ["STACKCHAN_TTS_URL"]
REMOTE_LLM_URL = os.environ["STACKCHAN_LLM_URL"]
REMOTE_LLM_MODEL = os.environ.get("STACKCHAN_LLM_MODEL", "").strip()
REMOTE_VOICE_LOCK_ID = os.environ.get("STACKCHAN_VOICE_LOCK_ID", "").strip()


def _assert_wav_audio(content: bytes) -> None:
    assert len(content) > 4096
    assert content[:4] == b"RIFF"
    with wave.open(io.BytesIO(content), "rb") as wav_file:
        assert wav_file.getnchannels() >= 1
        assert wav_file.getframerate() > 0
        assert wav_file.getnframes() > wav_file.getframerate()
        frames = wav_file.readframes(wav_file.getnframes())
    assert any(byte != 0 for byte in frames)


@pytest.mark.parametrize(
    ("text", "seed"),
    [
        ("おはよう、スタックちゃんです。", 12345),
        ("今日は元気だよ。", 12346),
        ("小さな妖精みたいな声の確認です。", 12347),
        ("起動直後のひとことを短く話します。", 12348),
        ("音声合成の連続試験をしています。", 12349),
    ],
)
def test_tts_api_repeated_requests_return_decodable_wav(text: str, seed: int):
    with httpx.Client(timeout=60) as client:
        payload = {
            "text": text,
            "instruct": VOICE_INSTRUCT,
            "seconds": 4.0,
            "num_steps": 20,
            "seed": seed,
        }
        if REMOTE_VOICE_LOCK_ID:
            payload["voice_lock_id"] = REMOTE_VOICE_LOCK_ID
        response = client.post(
            REMOTE_TTS_URL,
            json=payload,
        )
        response.raise_for_status()
    _assert_wav_audio(response.content)


def test_stt_api_accepts_wav_and_returns_text_field():
    with io.BytesIO() as buffer:
        with wave.open(buffer, "wb") as wav_file:
            wav_file.setnchannels(1)
            wav_file.setsampwidth(2)
            wav_file.setframerate(16000)
            wav_file.writeframes(b"\x00\x00" * 16000)
        wav_bytes = buffer.getvalue()

    with httpx.Client(timeout=60) as client:
        response = client.post(
            REMOTE_STT_URL,
            data={"language": "ja"},
            files={"file": ("silence.wav", wav_bytes, "audio/wav")},
        )
        response.raise_for_status()
        payload = response.json()

    assert "text" in payload
    assert isinstance(payload["text"], str)


def test_llm_api_returns_non_empty_content():
    user_text = "疎通確認です。正常応答という語を含めて一文だけ返してください。"

    with httpx.Client(timeout=60) as client:
        payload = {
            "messages": [
                {
                    "role": "system",
                    "content": "日本語で一文だけ返してください。思考過程タグは出さないでください。",
                },
                {"role": "user", "content": user_text},
            ],
            "temperature": 0,
            "max_tokens": 64,
        }
        if REMOTE_LLM_MODEL:
            payload["model"] = REMOTE_LLM_MODEL
        response = client.post(REMOTE_LLM_URL, json=payload)
        response.raise_for_status()
        payload = response.json()

    content = payload["choices"][0]["message"]["content"].strip()
    print(f"raw_llm_content={content!r}")
    assert content
    assert sanitize_llm_text(content, user_text)
