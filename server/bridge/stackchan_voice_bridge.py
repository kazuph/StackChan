import asyncio
import io
import json
import logging
import os
import re
import time
import uuid
import wave
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

import av
import httpx
import numpy as np
from fastapi import FastAPI, HTTPException, WebSocket, WebSocketDisconnect
from fastapi import Request

try:
    from server.bridge.stackchan_env import load_dotenv
except ModuleNotFoundError:
    from stackchan_env import load_dotenv


load_dotenv(Path(__file__).resolve().parent)

BRIDGE_HOST = os.environ.get("STACKCHAN_BRIDGE_HOST", "").strip()
BRIDGE_PORT = int(os.environ.get("STACKCHAN_BRIDGE_PORT", "8787"))
STT_URL = os.environ.get("STACKCHAN_STT_URL", "http://127.0.0.1:8088/api/stt/v1/stt")
TTS_URL = os.environ.get("STACKCHAN_TTS_URL", "http://127.0.0.1:8088/api/tts/v1/tts")
LLM_URL = os.environ.get("STACKCHAN_LLM_URL", "http://127.0.0.1:8088/api/llm/v1/chat/completions")
LLM_MODEL = os.environ.get("STACKCHAN_LLM_MODEL", "").strip()
LLM_API_KEY = os.environ.get("STACKCHAN_LLM_API_KEY", "").strip()
GEMINI_FALLBACK_URL = os.environ.get(
    "STACKCHAN_GEMINI_FALLBACK_URL",
    "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions",
).strip()
GEMINI_FALLBACK_MODEL = os.environ.get("STACKCHAN_GEMINI_FALLBACK_MODEL", "gemini-2.5-flash-lite").strip()
GEMINI_API_KEY = os.environ.get(
    "STACKCHAN_GEMINI_API_KEY",
    os.environ.get("GEMINI_API_KEY", os.environ.get("GOOGLE_API_KEY", "")),
).strip()
TIMEZONE_OFFSET_MINUTES = int(os.environ.get("STACKCHAN_TIMEZONE_OFFSET_MINUTES", "540"))
VOICE_LOCK_ID = os.environ.get("STACKCHAN_VOICE_LOCK_ID", "").strip()
TTS_SECONDS = float(os.environ.get("STACKCHAN_TTS_SECONDS", "4.0"))
TTS_NUM_STEPS = int(os.environ.get("STACKCHAN_TTS_NUM_STEPS", "20"))

INPUT_SAMPLE_RATE = 16000
OUTPUT_SAMPLE_RATE = 24000
OUTPUT_FRAME_DURATION_MS = 60
AUDIO_PACING_SECONDS = float(os.environ.get("STACKCHAN_AUDIO_PACING_SECONDS", str(OUTPUT_FRAME_DURATION_MS / 1000.0)))
AUDIO_PACING_AHEAD_PACKETS = int(os.environ.get("STACKCHAN_AUDIO_PACING_AHEAD_PACKETS", "3"))
MIN_TTS_SECONDS = float(os.environ.get("STACKCHAN_MIN_TTS_SECONDS", "5.5"))
MAX_TTS_SECONDS = float(os.environ.get("STACKCHAN_MAX_TTS_SECONDS", "18.0"))
TTS_SECONDS_PER_CHAR = float(os.environ.get("STACKCHAN_TTS_SECONDS_PER_CHAR", "0.22"))
TTS_RETRY_ATTEMPTS = int(os.environ.get("STACKCHAN_TTS_RETRY_ATTEMPTS", "3"))
TTS_RETRY_BACKOFF_SECONDS = float(os.environ.get("STACKCHAN_TTS_RETRY_BACKOFF_SECONDS", "0.75"))
ENABLE_LLM_END_DETECTION = os.environ.get("STACKCHAN_ENABLE_LLM_END_DETECTION", "").strip().lower() in {"1", "true", "yes", "on"}
VOICE_INSTRUCT = "小さな妖精みたいなAIの声で、自然で聞き取りやすい日本語で話してください。"
SYSTEM_PROMPT = (
    "あなたはスタックちゃんです。自分自身のことをスタックちゃんとして自然に話してください。"
    "家族は4人で、子どもは2人です。お姉ちゃんはこはるちゃん、弟はゆうくんです。"
    "この家族情報を会話の前提として扱ってください。"
    "返答は必ず短く、聞き取りやすい2文で答えてください。"
    "Markdown や箇条書きは使わず、そのまま読み上げられる文だけを返してください。"
    "思考過程タグ、XML風タグ、メタ説明は出さないでください。"
    "ユーザー発話の単純なオウム返しだけで終わらせないでください。"
)
REPETITION_RETRY_PROMPT = (
    "直前と同じ返答や同じ名乗りを繰り返さないでください。"
    "今回のユーザー発話にだけ短く直接答えてください。"
)
STARTUP_GREETING_PROMPT = (
    "あなたは起床直後のスタックちゃんです。"
    "電源が入った直後に自分でつぶやく短いひとことを、日本語で1文だけ作ってください。"
    "長さは最大24文字程度にしてください。"
    "例: よく寝た。 目が覚めたよ。 ふふふ、起きたよ。 シャキーン。"
    "明るく、少し茶目っ気があり、読み上げやすい文だけを返してください。"
    "Markdown、説明、括弧書き、思考過程は不要です。"
)
END_CONVERSATION_PROMPT = (
    "あなたは音声会話の終了判定器です。"
    "ユーザーが会話終了の意思を示していたら END、そうでなければ CONTINUE だけを返してください。"
    "例: お休み、終了、もういいよ、また明日、バイバイ は END です。"
)
END_KEYWORD_RE = re.compile(r"(おやすみ|お休み|終了|しゅうりょう|また明日|ばいばい|バイバイ|もういい|終わり)")
THINK_TAG_BLOCK_RE = re.compile(r"<think\b[^>]*>.*?</think>", re.IGNORECASE | re.DOTALL)
THINK_TAG_RE = re.compile(r"</?think\b[^>]*>", re.IGNORECASE)
WHITESPACE_RE = re.compile(r"\s+")
COMPARE_NORMALIZE_RE = re.compile(r"[\s\u3000、。！？!?…,.「」『』（）()\-]+")


app = FastAPI(title="stackchan-voice-bridge")
http_client = httpx.AsyncClient(timeout=httpx.Timeout(120.0, connect=10.0))
logger = logging.getLogger("stackchan-voice-bridge")
active_session: "BridgeSession | None" = None
logger.setLevel(logging.INFO)
if not logger.handlers:
    log_path = Path(os.environ.get("STACKCHAN_BRIDGE_EVENT_LOG", str(Path.home() / "stackchan_voice_bridge.events.log")))
    file_handler = logging.FileHandler(log_path, encoding="utf-8")
    file_handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(message)s"))
    logger.addHandler(file_handler)
    logger.propagate = False


def _iter_frames(resampled: Any):
    if resampled is None:
        return []
    if isinstance(resampled, list):
        return resampled
    return [resampled]


def _frame_to_pcm_bytes(frame: av.AudioFrame) -> bytes:
    array = frame.to_ndarray()
    if np.issubdtype(array.dtype, np.floating):
        array = np.clip(array, -1.0, 1.0)
        array = (array * 32767.0).astype(np.int16)
    elif array.dtype != np.int16:
        array = array.astype(np.int16)
    return array.reshape(-1).tobytes()


def opus_packets_to_wav_bytes(packets: list[bytes], sample_rate: int = INPUT_SAMPLE_RATE) -> bytes:
    decoder = av.codec.CodecContext.create("opus", "r")
    resampler = av.AudioResampler(format="s16", layout="mono", rate=sample_rate)
    pcm_chunks: list[bytes] = []

    for payload in packets:
        packet = av.Packet(payload)
        for frame in decoder.decode(packet):
            for resampled in _iter_frames(resampler.resample(frame)):
                pcm_chunks.append(_frame_to_pcm_bytes(resampled))

    for frame in decoder.decode(None):
        for resampled in _iter_frames(resampler.resample(frame)):
            pcm_chunks.append(_frame_to_pcm_bytes(resampled))

    with io.BytesIO() as buffer:
        with wave.open(buffer, "wb") as wav_file:
            wav_file.setnchannels(1)
            wav_file.setsampwidth(2)
            wav_file.setframerate(sample_rate)
            wav_file.writeframes(b"".join(pcm_chunks))
        return buffer.getvalue()


def wav_bytes_to_opus_packets(wav_bytes: bytes, sample_rate: int = OUTPUT_SAMPLE_RATE) -> list[bytes]:
    input_buffer = io.BytesIO(wav_bytes)
    container = av.open(input_buffer)
    stream = container.streams.audio[0]

    encoder = av.codec.CodecContext.create("libopus", "w")
    encoder.sample_rate = sample_rate
    encoder.layout = "mono"
    encoder.format = "s16"
    encoder.bit_rate = 32000
    encoder.options = {
        "application": "voip",
        "frame_duration": str(OUTPUT_FRAME_DURATION_MS),
        "vbr": "off",
    }

    resampler = av.AudioResampler(format="s16", layout="mono", rate=sample_rate)
    packets: list[bytes] = []

    for frame in container.decode(stream):
        for resampled in _iter_frames(resampler.resample(frame)):
            for packet in encoder.encode(resampled):
                packets.append(bytes(packet))

    for packet in encoder.encode(None):
        packets.append(bytes(packet))

    container.close()
    return packets


async def run_stt(wav_bytes: bytes) -> str:
    response = await http_client.post(
        STT_URL,
        data={"language": "ja"},
        files={"file": ("stackchan.wav", wav_bytes, "audio/wav")},
    )
    response.raise_for_status()
    payload = response.json()
    return payload.get("text", "").strip()


def _llm_headers(api_key: str) -> dict[str, str] | None:
    if not api_key:
        return None
    return {"Authorization": f"Bearer {api_key}"}


async def _post_llm(url: str, payload: dict[str, Any], api_key: str = "") -> dict[str, Any]:
    request_kwargs: dict[str, Any] = {"json": payload}
    headers = _llm_headers(api_key)
    if headers:
        request_kwargs["headers"] = headers
    response = await http_client.post(url, **request_kwargs)
    response.raise_for_status()
    return response.json()


async def _request_llm_completion(payload: dict[str, Any]) -> dict[str, Any]:
    primary_error: Exception | None = None
    primary_payload = dict(payload)
    if LLM_MODEL:
        primary_payload["model"] = LLM_MODEL
    try:
        return await _post_llm(LLM_URL, primary_payload, api_key=LLM_API_KEY)
    except Exception as exc:
        primary_error = exc
        logger.warning("llm_primary_failed url=%s model=%s error=%s", LLM_URL, LLM_MODEL or "<default>", exc)

    if GEMINI_API_KEY and GEMINI_FALLBACK_URL and GEMINI_FALLBACK_MODEL:
        fallback_payload = dict(payload)
        fallback_payload["model"] = GEMINI_FALLBACK_MODEL
        fallback_payload.setdefault("reasoning_effort", "minimal")
        logger.info("llm_fallback_to_gemini model=%s", GEMINI_FALLBACK_MODEL)
        return await _post_llm(GEMINI_FALLBACK_URL, fallback_payload, api_key=GEMINI_API_KEY)

    if primary_error is not None:
        raise primary_error
    raise RuntimeError("LLM request failed without configured fallback")


async def run_llm(history: list[dict[str, str]], user_text: str, extra_system_prompt: str = "") -> str:
    system_prompt = SYSTEM_PROMPT if not extra_system_prompt else f"{SYSTEM_PROMPT}{extra_system_prompt}"
    messages = [{"role": "system", "content": system_prompt}]
    messages.extend(history)
    messages.append({"role": "user", "content": user_text})
    payload = {
        "messages": messages,
        "temperature": 0.6,
        "max_tokens": 256,
        "stop": ["<think>", "</think>"],
    }
    response_payload = await _request_llm_completion(payload)
    return response_payload["choices"][0]["message"]["content"].strip()


async def run_startup_greeting_llm() -> str:
    payload = {
        "messages": [{"role": "system", "content": STARTUP_GREETING_PROMPT}],
        "temperature": 0.9,
        "max_tokens": 64,
        "stop": ["<think>", "</think>", "\n"],
    }
    response_payload = await _request_llm_completion(payload)
    return response_payload["choices"][0]["message"]["content"].strip()


async def should_end_conversation(history: list[dict[str, str]], user_text: str) -> bool:
    if is_exit_phrase(user_text):
        return True
    if not ENABLE_LLM_END_DETECTION:
        return False
    messages = [{"role": "system", "content": END_CONVERSATION_PROMPT}]
    messages.extend(history)
    messages.append({"role": "user", "content": user_text})
    payload = {
        "messages": messages,
        "temperature": 0,
        "max_tokens": 8,
        "stop": ["\n"],
    }
    response_payload = await _request_llm_completion(payload)
    verdict = response_payload["choices"][0]["message"]["content"].strip().upper()
    return verdict.startswith("END")


async def safe_should_end_conversation(history: list[dict[str, str]], user_text: str) -> bool:
    try:
        return await should_end_conversation(history, user_text)
    except Exception as exc:
        logger.warning("end_conversation_check_failed user_text=%r error=%s", user_text, exc)
        return False


def normalize_compare_text(text: str) -> str:
    return COMPARE_NORMALIZE_RE.sub("", text).strip()


def is_exit_phrase(text: str) -> bool:
    return END_KEYWORD_RE.search(text) is not None


def sanitize_llm_text(text: str, user_text: str) -> str:
    cleaned = THINK_TAG_BLOCK_RE.sub("", text)
    cleaned = THINK_TAG_RE.sub("", cleaned)
    cleaned = WHITESPACE_RE.sub(" ", cleaned).strip()
    if not cleaned:
        return ""
    if normalize_compare_text(cleaned) == normalize_compare_text(user_text):
        return ""
    return cleaned


def sanitize_startup_greeting(text: str) -> str:
    cleaned = THINK_TAG_BLOCK_RE.sub("", text)
    cleaned = THINK_TAG_RE.sub("", cleaned)
    cleaned = WHITESPACE_RE.sub(" ", cleaned).strip()
    return cleaned


def find_last_message(history: list[dict[str, str]], role: str) -> str:
    for message in reversed(history):
        if message.get("role") == role:
            return message.get("content", "")
    return ""


def is_repetitive_answer(history: list[dict[str, str]], user_text: str, answer: str) -> bool:
    if not answer:
        return False
    previous_answer = find_last_message(history, "assistant")
    if not previous_answer:
        return False
    if normalize_compare_text(previous_answer) != normalize_compare_text(answer):
        return False
    previous_user = find_last_message(history, "user")
    return normalize_compare_text(previous_user) != normalize_compare_text(user_text)


def estimate_tts_seconds(text: str) -> float:
    visible_chars = len(WHITESPACE_RE.sub("", text))
    estimated = max(MIN_TTS_SECONDS, visible_chars * TTS_SECONDS_PER_CHAR)
    return min(MAX_TTS_SECONDS, round(estimated, 2))


def user_safe_alert_message(exc: Exception) -> str:
    if isinstance(exc, httpx.HTTPStatusError):
        request_url = str(exc.request.url)
        if "/api/tts/" in request_url:
            return "音声合成でエラーが出たよ。もう一度ためしてね。"
        if "/api/stt/" in request_url:
            return "聞き取りでエラーが出たよ。もう一度話してね。"
        if "/api/llm/" in request_url or "/chat/completions" in request_url:
            return "考えるところでエラーが出たよ。もう一度ためしてね。"
    if isinstance(exc, httpx.RequestError):
        request_url = str(exc.request.url)
        if "/api/tts/" in request_url:
            return "音声合成につながらないよ。少し待ってね。"
        if "/api/stt/" in request_url:
            return "聞き取りにつながらないよ。少し待ってね。"
        if "/api/llm/" in request_url or "/chat/completions" in request_url:
            return "考えるところにつながらないよ。少し待ってね。"
    return "橋渡しでエラーが出たよ。もう一度ためしてね。"


def summarize_http_error(exc: httpx.HTTPStatusError) -> str:
    body = exc.response.text.strip()
    if len(body) > 240:
        body = f"{body[:240]}..."
    return body


async def run_tts(text: str) -> bytes:
    requested_seconds = estimate_tts_seconds(text)
    payload: dict[str, Any] = {
        "text": text,
        "seconds": requested_seconds,
        "num_steps": TTS_NUM_STEPS,
    }
    if VOICE_LOCK_ID:
        payload["voice_lock_id"] = VOICE_LOCK_ID
    else:
        payload["instruct"] = VOICE_INSTRUCT
    for attempt in range(1, TTS_RETRY_ATTEMPTS + 1):
        try:
            response = await http_client.post(
                TTS_URL,
                json=payload,
            )
            response.raise_for_status()
            return response.content
        except httpx.HTTPStatusError as exc:
            logger.warning(
                "tts_http_failed attempt=%d/%d status=%d seconds=%.2f text=%r body=%r",
                attempt,
                TTS_RETRY_ATTEMPTS,
                exc.response.status_code,
                requested_seconds,
                text,
                summarize_http_error(exc),
            )
            if exc.response.status_code not in {502, 503, 504} or attempt >= TTS_RETRY_ATTEMPTS:
                raise
        except httpx.RequestError as exc:
            logger.warning(
                "tts_request_failed attempt=%d/%d seconds=%.2f text=%r error=%s",
                attempt,
                TTS_RETRY_ATTEMPTS,
                requested_seconds,
                text,
                exc,
            )
            if attempt >= TTS_RETRY_ATTEMPTS:
                raise
        await asyncio.sleep(TTS_RETRY_BACKOFF_SECONDS * attempt)
    raise RuntimeError("unreachable")


@dataclass
class BridgeSession:
    websocket: WebSocket
    session_id: str = field(default_factory=lambda: str(uuid.uuid4()))
    history: list[dict[str, str]] = field(default_factory=list)
    listening: bool = False
    input_packets: list[bytes] = field(default_factory=list)
    response_task: asyncio.Task | None = None
    send_lock: asyncio.Lock = field(default_factory=asyncio.Lock)
    audio_packet_count: int = 0
    pending_end_conversation: bool = False
    closed: bool = False

    async def send_json(self, payload: dict[str, Any]) -> None:
        if self.closed:
            return
        async with self.send_lock:
            if self.closed:
                return
            try:
                await self.websocket.send_text(json.dumps(payload, ensure_ascii=False))
            except (WebSocketDisconnect, RuntimeError):
                self.closed = True

    async def send_audio(self, payload: bytes) -> None:
        if self.closed:
            return
        async with self.send_lock:
            if self.closed:
                return
            try:
                await self.websocket.send_bytes(payload)
            except (WebSocketDisconnect, RuntimeError):
                self.closed = True

    async def send_audio_stream(self, packets: list[bytes]) -> None:
        if not packets:
            return
        loop = asyncio.get_running_loop()
        stream_start = loop.time()
        for index, payload in enumerate(packets):
            if self.closed:
                return
            paced_index = max(0, index - AUDIO_PACING_AHEAD_PACKETS)
            target_time = stream_start + (paced_index * AUDIO_PACING_SECONDS)
            delay = target_time - loop.time()
            if delay > 0:
                await asyncio.sleep(delay)
            await self.send_audio(payload)

    async def cancel_response(self) -> None:
        if self.response_task is None:
            return
        task = self.response_task
        self.response_task = None
        task.cancel()
        try:
            await task
        except asyncio.CancelledError:
            pass
        await self.send_json({"type": "tts", "state": "stop"})

    def spawn_response(self, coro: Any) -> None:
        async def runner():
            try:
                await coro
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                logger.exception("session=%s bridge_response_failed", self.session_id)
                await self.send_json(
                    {
                        "type": "alert",
                        "status": "Bridge Error",
                        "message": user_safe_alert_message(exc),
                        "emotion": "sad",
                    }
                )
                await self.send_json({"type": "tts", "state": "stop"})
            finally:
                if self.response_task is task:
                    self.response_task = None

        task = asyncio.create_task(runner())
        self.response_task = task

    async def start_listening(self) -> None:
        await self.cancel_response()
        self.listening = True
        self.input_packets.clear()
        self.audio_packet_count = 0

    def trigger_startup_greeting(self) -> None:
        self.spawn_response(self.send_startup_greeting())

    def trigger_manual_speech(self, text: str) -> None:
        async def runner() -> None:
            await self.cancel_response()
            await self.respond(text, from_stt=False)

        self.spawn_response(runner())

    def append_audio_packet(self, payload: bytes) -> None:
        if self.listening:
            self.input_packets.append(payload)
            self.audio_packet_count += 1

    async def stop_listening(self) -> None:
        self.listening = False
        packets = list(self.input_packets)
        self.input_packets.clear()
        logger.info("session=%s stop_listening packets=%d", self.session_id, self.audio_packet_count)
        if not packets:
            self.spawn_response(self.respond("聞こえませんでした。もう一度お願いします。", from_stt=False))
            return
        self.spawn_response(self.handle_turn(packets))

    async def handle_turn(self, packets: list[bytes]) -> None:
        wav_bytes = await asyncio.to_thread(opus_packets_to_wav_bytes, packets)
        user_text = await run_stt(wav_bytes)
        logger.info("session=%s stt=%r", self.session_id, user_text)
        if not user_text:
            await self.respond("聞こえませんでした。もう一度お願いします。", from_stt=False)
            return
        await self.send_json({"type": "stt", "text": user_text})
        self.pending_end_conversation = await safe_should_end_conversation(self.history, user_text)
        logger.info("session=%s end_conversation=%d", self.session_id, int(self.pending_end_conversation))
        try:
            raw_answer = await run_llm(self.history, user_text)
            answer = sanitize_llm_text(raw_answer, user_text)
            if is_repetitive_answer(self.history, user_text, answer):
                logger.info("session=%s repeating_answer_retry previous=%r", self.session_id, answer)
                raw_answer = await run_llm(self.history, user_text, extra_system_prompt=REPETITION_RETRY_PROMPT)
                answer = sanitize_llm_text(raw_answer, user_text)
        except Exception as exc:
            logger.warning("session=%s llm_failed error=%s", self.session_id, exc)
            raw_answer = ""
            answer = ""
        logger.info("session=%s llm_raw=%r", self.session_id, raw_answer)
        logger.info("session=%s llm_sanitized=%r", self.session_id, answer)
        if not answer:
            answer = "おやすみなさい。またね。" if self.pending_end_conversation else "ごめんね。今ちょっと考え中だよ。"
        self.history.append({"role": "user", "content": user_text})
        self.history.append({"role": "assistant", "content": answer})
        await self.respond(answer, from_stt=True)

    async def send_startup_greeting(self) -> None:
        try:
            raw_text = await run_startup_greeting_llm()
        except Exception as exc:
            logger.warning("session=%s startup_llm_failed error=%s", self.session_id, exc)
            raw_text = ""
        text = sanitize_startup_greeting(raw_text) or "ふふ、起きたよ。"
        logger.info("session=%s startup_raw=%r", self.session_id, raw_text)
        logger.info("session=%s startup_text=%r", self.session_id, text)
        await self.respond(text, from_stt=True)

    async def respond(self, text: str, from_stt: bool) -> None:
        if not text.strip():
            text = "すみません。うまく答えを作れませんでした。"
        logger.info("session=%s respond=%r", self.session_id, text)
        if not from_stt:
            await self.send_json({"type": "stt", "text": ""})
        await self.send_json({"type": "llm", "emotion": "neutral"})
        await self.send_json({"type": "tts", "state": "start"})
        await self.send_json({"type": "tts", "state": "sentence_start", "text": text})
        wav_bytes = await run_tts(text)
        opus_packets = await asyncio.to_thread(wav_bytes_to_opus_packets, wav_bytes)
        logger.info(
            "session=%s tts_packets=%d approx_duration_sec=%.2f pacing_sec=%.3f ahead_packets=%d",
            self.session_id,
            len(opus_packets),
            len(opus_packets) * AUDIO_PACING_SECONDS,
            AUDIO_PACING_SECONDS,
            AUDIO_PACING_AHEAD_PACKETS,
        )
        await self.send_audio_stream(opus_packets)
        if self.pending_end_conversation:
            await self.send_json({"type": "system", "command": "end_conversation"})
            self.pending_end_conversation = False
        await self.send_json({"type": "tts", "state": "stop"})


def resolve_bridge_host(request: Request | None = None) -> str:
    if BRIDGE_HOST:
        return BRIDGE_HOST
    if request is not None:
        forwarded_host = request.headers.get("x-forwarded-host", "").strip()
        if forwarded_host:
            return forwarded_host.split(",")[0].strip()
        host = request.url.hostname
        if host:
            return host
    return "127.0.0.1"


@app.api_route("/xiaozhi/ota/", methods=["GET", "POST"])
async def ota_config(request: Request):
    now_ms = int(time.time() * 1000)
    bridge_host = resolve_bridge_host(request)
    return {
        "websocket": {
            "url": f"ws://{bridge_host}:{BRIDGE_PORT}/xiaozhi/ws",
            "token": "",
            "version": 1,
        },
        "server_time": {
            "timestamp": now_ms,
            "timezone_offset": TIMEZONE_OFFSET_MINUTES,
        },
    }


@app.get("/healthz")
async def healthz():
    return {
        "status": "ok",
        "bridge_host": BRIDGE_HOST or None,
        "bridge_port": BRIDGE_PORT,
        "stt_url": STT_URL,
        "tts_url": TTS_URL,
        "llm_url": LLM_URL,
    }


@app.post("/speak")
async def speak(payload: dict[str, Any]):
    session = active_session
    text = str(payload.get("text", "")).strip()
    if not text:
        raise HTTPException(status_code=400, detail="text is required")
    if session is None or session.closed:
        raise HTTPException(status_code=409, detail="no active device session")
    session.trigger_manual_speech(text)
    return {"status": "queued", "session_id": session.session_id, "text": text}


@app.websocket("/xiaozhi/ws")
async def websocket_endpoint(websocket: WebSocket):
    await websocket.accept()
    session = BridgeSession(websocket=websocket)
    global active_session
    active_session = session
    logger.info("session=%s accepted", session.session_id)

    try:
        first_message = await asyncio.wait_for(websocket.receive(), timeout=5)
        logger.info("session=%s first_message keys=%s", session.session_id, list(first_message.keys()))
        hello = first_message.get("text")
        if hello is None:
            logger.warning("session=%s non_text_first_message", session.session_id)
            await websocket.close(code=1003)
            return
        hello_payload = json.loads(hello)
        if hello_payload.get("type") != "hello":
            logger.warning("session=%s invalid_hello payload=%s", session.session_id, hello_payload)
            await websocket.close(code=1003)
            return
        logger.info("session=%s connected hello=%s", session.session_id, hello_payload)

        await session.send_json(
            {
                "type": "hello",
                "transport": "websocket",
                "session_id": session.session_id,
                "audio_params": {
                    "format": "opus",
                    "sample_rate": OUTPUT_SAMPLE_RATE,
                    "channels": 1,
                    "frame_duration": OUTPUT_FRAME_DURATION_MS,
                },
            }
        )
        session.trigger_startup_greeting()

        while True:
            message = await websocket.receive()
            if message.get("bytes") is not None:
                session.append_audio_packet(message["bytes"])
                continue

            text = message.get("text")
            if text is None:
                continue

            payload = json.loads(text)
            message_type = payload.get("type")
            if message_type == "listen":
                state = payload.get("state")
                logger.info("session=%s listen state=%s mode=%s", session.session_id, state, payload.get("mode"))
                if state == "start":
                    await session.start_listening()
                elif state == "stop":
                    await session.stop_listening()
            elif message_type == "abort":
                logger.info("session=%s abort", session.session_id)
                await session.cancel_response()
            elif message_type == "mcp":
                await session.send_json(
                    {
                        "type": "mcp",
                        "payload": {
                            "type": "error",
                            "message": "MCP bridge is not implemented yet.",
                        },
                    }
                )
    except asyncio.TimeoutError:
        logger.warning("session=%s hello_timeout", session.session_id)
    except (WebSocketDisconnect, RuntimeError):
        pass
    finally:
        session.closed = True
        if active_session is session:
            active_session = None
        logger.info("session=%s closed", session.session_id)
        await session.cancel_response()


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=BRIDGE_PORT)
