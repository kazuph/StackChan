import asyncio
import os
from pathlib import Path

import httpx
import pytest
from fastapi import Request

from server.bridge import stackchan_voice_bridge as bridge
from server.bridge.stackchan_env import load_dotenv
from server.bridge.stackchan_voice_bridge import (
    BridgeSession,
    ENABLE_LLM_END_DETECTION,
    LLM_MAX_TOKENS,
    SYSTEM_PROMPT,
    fallback_ir_action_speech,
    ir_action_facts,
    ir_effective_manufacturer,
    is_repetitive_answer,
    is_exit_phrase,
    resolve_bridge_host,
    should_end_conversation,
    run_llm,
    run_tts,
    sanitize_display_transcript,
    sanitize_llm_text,
    sanitize_startup_greeting,
    summarize_http_error,
    tts_readable_text,
    usable_ir_llm_speech,
)


def test_sanitize_llm_text_strips_think_tags():
    raw = "<think>internal reasoning</think>\n\nこんにちは。"

    assert sanitize_llm_text(raw, "元気?") == "こんにちは。"


def test_sanitize_llm_text_drops_parrot_reply():
    assert sanitize_llm_text("こんにちは", "こんにちは") == ""


def test_sanitize_llm_text_keeps_short_reply():
    raw = "スタックちゃんだよ。よろしくね。3文目は切ってね。"

    assert sanitize_llm_text(raw, "自己紹介して") == "スタックちゃんだよ。よろしくね。3文目は切ってね。"


def test_sanitize_llm_text_does_not_truncate_mid_sentence():
    raw = "今日は少し長めに話すね。まだ続きがあるよ。最後まで自然に読み上げてね。"

    assert sanitize_llm_text(raw, "なにしてるの?") == raw


def test_tts_readable_text_reads_ir_protocol_names_in_japanese():
    text = "メーカーはPANASONIC、プロトコルはPANASONIC_AC。DAIKINも検知したよ。"

    assert tts_readable_text(text) == "メーカーはパナソニック、プロトコルはパナソニック エーシー。ダイキンも検知したよ。"


def test_tts_readable_text_spells_unknown_alpha_numeric_tokens():
    assert tts_readable_text("ABC12を受信したよ。") == "エービーシーイチニを受信したよ。"


def test_ir_effective_manufacturer_falls_back_to_protocol_prefix():
    assert ir_effective_manufacturer({"manufacturer": "Unknown", "protocol": "DAIKIN"}) == "DAIKIN"
    assert ir_effective_manufacturer({"manufacturer": "Unknown", "protocol": "PANASONIC_AC"}) == "PANASONIC"


def test_ir_effective_manufacturer_ignores_non_aircon_protocols():
    assert ir_effective_manufacturer({"manufacturer": "MULTIBRACKETS", "protocol": "MULTIBRACKETS"}) == ""
    assert ir_effective_manufacturer({"manufacturer": "Unknown", "protocol": "MULTIBRACKETS"}) == ""


def test_ir_action_facts_extracts_aircon_controls():
    facts = ir_action_facts({"decoded": {"power": True, "mode": "cool", "temperatureC": 26, "fan": "auto"}})

    assert facts == ["運転オン", "モード=冷房", "温度=26度", "風量=自動"]


def test_fallback_ir_action_speech_prefers_mode_and_temperature():
    assert fallback_ir_action_speech(["運転オン", "モード=暖房", "温度=24度"]) == "暖房を24度にしたよ。"


def test_usable_ir_llm_speech_rejects_too_short_reply():
    assert usable_ir_llm_speech("冷房") == ""


def test_is_exit_phrase_detects_good_night():
    assert is_exit_phrase("じゃあお休み")


def test_sanitize_startup_greeting_keeps_one_short_sentence():
    raw = "<think>foo</think>ふふふ、起きたよ。今日はいい日だよ。"

    assert sanitize_startup_greeting(raw) == "ふふふ、起きたよ。今日はいい日だよ。"


def test_is_repetitive_answer_detects_same_reply_for_new_question():
    history = [
        {"role": "user", "content": "君の名前は?"},
        {"role": "assistant", "content": "スタックちゃんだよ。"},
    ]

    assert is_repetitive_answer(history, "1足す1は?", "スタックちゃんだよ。")


def test_is_repetitive_answer_allows_same_reply_for_same_question():
    history = [
        {"role": "user", "content": "君の名前は?"},
        {"role": "assistant", "content": "スタックちゃんだよ。"},
    ]

    assert not is_repetitive_answer(history, "君の名前は?", "スタックちゃんだよ。")


def test_run_llm_uses_full_history(monkeypatch: pytest.MonkeyPatch):
    captured = {}

    async def fake_post(url, json, headers=None):
        captured["messages"] = json["messages"]
        captured["max_tokens"] = json["max_tokens"]
        request = httpx.Request("POST", url)
        return httpx.Response(
            200,
            request=request,
            json={"choices": [{"message": {"content": "了解だよ。"}}]},
        )

    monkeypatch.setattr(bridge.http_client, "post", fake_post)

    history = [
        {"role": "user", "content": "一個目"},
        {"role": "assistant", "content": "返答一個目"},
        {"role": "user", "content": "二個目"},
        {"role": "assistant", "content": "返答二個目"},
        {"role": "user", "content": "三個目"},
        {"role": "assistant", "content": "返答三個目"},
        {"role": "user", "content": "四個目"},
        {"role": "assistant", "content": "返答四個目"},
    ]

    result = asyncio.run(run_llm(history, "最新の質問"))
    assert result == "了解だよ。"
    assert captured["messages"][1:-1] == history
    assert captured["max_tokens"] == 2048


def test_system_prompt_reflects_speech_context_and_family_roles():
    assert "スタックちゃん自身に子どもはいません" in SYSTEM_PROMPT
    assert "かずさん" in SYSTEM_PROMPT
    assert "ちひろさん" in SYSTEM_PROMPT
    assert "こはたん" in SYSTEM_PROMPT
    assert "ゆうくん" in SYSTEM_PROMPT
    assert "どちらも小学生" in SYSTEM_PROMPT
    assert "Whisper" in SYSTEM_PROMPT


def test_llm_max_tokens_default_is_2048():
    assert LLM_MAX_TOKENS == 2048


def test_run_llm_falls_back_to_gemini_when_primary_fails(monkeypatch: pytest.MonkeyPatch):
    calls = []

    async def fake_post(url, json, headers=None):
        calls.append((url, json, headers))
        request = httpx.Request("POST", url)
        if url == "http://primary.invalid/chat/completions":
            return httpx.Response(503, request=request, text="primary down")
        return httpx.Response(200, request=request, json={"choices": [{"message": {"content": "Gemini だよ。"}}]})

    monkeypatch.setattr(bridge.http_client, "post", fake_post)
    monkeypatch.setattr(bridge, "LLM_URL", "http://primary.invalid/chat/completions")
    monkeypatch.setattr(bridge, "LLM_MODEL", "primary-model")
    monkeypatch.setattr(bridge, "LLM_API_KEY", "")
    monkeypatch.setattr(bridge, "GEMINI_FALLBACK_URL", "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions")
    monkeypatch.setattr(bridge, "GEMINI_FALLBACK_MODEL", "gemini-2.5-flash-lite")
    monkeypatch.setattr(bridge, "GEMINI_API_KEY", "gemini-test-key")

    result = asyncio.run(run_llm([], "こんにちは"))

    assert result == "Gemini だよ。"
    assert len(calls) == 2
    assert calls[1][1]["model"] == "gemini-2.5-flash-lite"
    assert calls[1][1]["reasoning_effort"] == "minimal"
    assert calls[1][2] == {"Authorization": "Bearer gemini-test-key"}


def test_run_tts_retries_transient_gateway_errors(monkeypatch: pytest.MonkeyPatch):
    request = httpx.Request("POST", bridge.TTS_URL)
    responses = [
        httpx.Response(502, request=request, text="bad gateway"),
        httpx.Response(200, request=request, content=b"wav-bytes"),
    ]

    async def fake_post(url, json, headers=None):
        return responses.pop(0)

    async def fake_sleep(_delay):
        return None

    monkeypatch.setattr(bridge.http_client, "post", fake_post)
    monkeypatch.setattr(bridge.asyncio, "sleep", fake_sleep)

    assert asyncio.run(run_tts("テストだよ。")) == b"wav-bytes"


def test_summarize_http_error_truncates_long_body():
    request = httpx.Request("POST", bridge.TTS_URL)
    response = httpx.Response(502, request=request, text="x" * 400)
    exc = httpx.HTTPStatusError("boom", request=request, response=response)

    assert summarize_http_error(exc).endswith("...")


def test_should_end_conversation_uses_keyword_without_llm():
    assert asyncio.run(should_end_conversation([], "じゃあお休み")) is True


def test_should_end_conversation_does_not_end_on_polite_closing_when_llm_detection_disabled(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setattr(bridge, "ENABLE_LLM_END_DETECTION", False)

    async def fail_post(*_args, **_kwargs):
        raise AssertionError("LLM end detection should not run")

    monkeypatch.setattr(bridge.http_client, "post", fail_post)

    assert asyncio.run(should_end_conversation([], "お疲れ様でした。")) is False


def test_send_startup_greeting_returns_to_idle_after_tts(monkeypatch: pytest.MonkeyPatch):
    session = BridgeSession(websocket=None)  # type: ignore[arg-type]
    sent_payloads = []

    async def fake_startup():
        return "ふふ、起きたよ。"

    async def fake_tts(_text: str):
        return b"wav"

    def fake_opus(_wav: bytes):
        return [b"opus"]

    async def fake_send_json(payload):
        sent_payloads.append(payload)

    async def fake_send_audio_stream(_packets):
        return None

    monkeypatch.setattr(bridge, "run_startup_greeting_llm", fake_startup)
    monkeypatch.setattr(bridge, "run_tts", fake_tts)
    monkeypatch.setattr(bridge, "wav_bytes_to_opus_packets", fake_opus)
    monkeypatch.setattr(session, "send_json", fake_send_json)
    monkeypatch.setattr(session, "send_audio_stream", fake_send_audio_stream)

    asyncio.run(session.send_startup_greeting())

    assert sent_payloads[-2:] == [
        {"type": "system", "command": "idle_after_tts"},
        {"type": "tts", "state": "stop"},
    ]


def test_respond_uses_readable_text_for_tts_but_keeps_display_text(monkeypatch: pytest.MonkeyPatch):
    session = BridgeSession(websocket=None)  # type: ignore[arg-type]
    sent_payloads = []
    tts_inputs = []

    async def fake_tts(text: str):
        tts_inputs.append(text)
        return b"wav"

    def fake_opus(_wav: bytes):
        return [b"opus"]

    async def fake_send_json(payload):
        sent_payloads.append(payload)

    async def fake_send_audio_stream(_packets):
        return None

    monkeypatch.setattr(bridge, "run_tts", fake_tts)
    monkeypatch.setattr(bridge, "wav_bytes_to_opus_packets", fake_opus)
    monkeypatch.setattr(session, "send_json", fake_send_json)
    monkeypatch.setattr(session, "send_audio_stream", fake_send_audio_stream)

    asyncio.run(session.respond("メーカーはPANASONIC、プロトコルはPANASONIC_AC。", from_stt=False))

    assert {"type": "tts", "state": "sentence_start", "text": "メーカーはPANASONIC、プロトコルはPANASONIC_AC。"} in sent_payloads
    assert tts_inputs == ["メーカーはパナソニック、プロトコルはパナソニック エーシー。"]


def test_ir_decode_speech_mentions_manufacturer_only_when_changed(monkeypatch: pytest.MonkeyPatch):
    session = BridgeSession(websocket=None)  # type: ignore[arg-type]

    async def fake_ir_event_llm(facts):
        return "冷房を26度にしたよ。"

    monkeypatch.setattr(bridge, "run_ir_event_llm", fake_ir_event_llm)

    first = asyncio.run(
        session.build_ir_decode_speech(
            {"manufacturer": "Panasonic", "protocol": "PANASONIC_AC", "decoded": {"power": True, "mode": "cool", "temperatureC": 26}}
        )
    )
    second = asyncio.run(
        session.build_ir_decode_speech(
            {"manufacturer": "Panasonic", "protocol": "PANASONIC_AC", "decoded": {"power": True, "mode": "cool", "temperatureC": 26}}
        )
    )
    changed = asyncio.run(
        session.build_ir_decode_speech(
            {"manufacturer": "Daikin", "protocol": "DAIKIN", "decoded": {"power": True, "mode": "heat", "temperatureC": 24}}
        )
    )

    assert first == ("manufacturer_changed", "メーカーがPanasonicに切り替わったよ。")
    assert second == ("action", "冷房を26度にしたよ。")
    assert changed == ("manufacturer_changed", "メーカーがDaikinに切り替わったよ。")


def test_ir_decode_speech_stays_silent_without_action_after_known_manufacturer():
    session = BridgeSession(websocket=None)  # type: ignore[arg-type]
    session.last_ir_manufacturer = "Panasonic"

    result = asyncio.run(session.build_ir_decode_speech({"manufacturer": "Panasonic", "protocol": "PANASONIC_AC", "decoded": {}}))

    assert result == ("silent", "")


def test_ir_decode_speech_ignores_multibrackets():
    session = BridgeSession(websocket=None)  # type: ignore[arg-type]

    result = asyncio.run(session.build_ir_decode_speech({"manufacturer": "MULTIBRACKETS", "protocol": "MULTIBRACKETS", "decoded": {}}))

    assert result == ("silent", "")
    assert session.last_ir_manufacturer == ""


def test_trigger_manual_speech_marks_idle_after_tts(monkeypatch: pytest.MonkeyPatch):
    session = BridgeSession(websocket=None)  # type: ignore[arg-type]
    recorded = {}

    async def fake_cancel_response():
        return None

    async def fake_respond(text: str, from_stt: bool):
        recorded["text"] = text
        recorded["from_stt"] = from_stt

    def fake_spawn_response(coro):
        asyncio.run(coro)

    monkeypatch.setattr(session, "cancel_response", fake_cancel_response)
    monkeypatch.setattr(session, "respond", fake_respond)
    monkeypatch.setattr(session, "spawn_response", fake_spawn_response)

    session.trigger_manual_speech("こんにちは")

    assert session.pending_idle_after_tts is True
    assert recorded == {"text": "こんにちは", "from_stt": False}


def test_handle_missed_input_stops_after_two_times(monkeypatch: pytest.MonkeyPatch):
    session = BridgeSession(websocket=None)  # type: ignore[arg-type]
    calls = []

    async def fake_respond(text: str, from_stt: bool):
        calls.append((text, from_stt, session.pending_idle_after_tts))

    monkeypatch.setattr(session, "respond", fake_respond)

    asyncio.run(session.handle_missed_input())
    asyncio.run(session.handle_missed_input())

    assert calls == [
        ("聞こえませんでした。もう一度お願いします。", False, False),
        ("聞こえませんでした。いったん終わるね。", False, True),
    ]
    assert session.consecutive_no_input_count == 2


def test_sanitize_display_transcript_removes_whitespace():
    assert sanitize_display_transcript(" しり とり\nして　いい? ") == "しりとりしていい?"


def test_resolve_bridge_host_prefers_request_host(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setattr(bridge, "BRIDGE_HOST", "")
    request = Request(
        {
            "type": "http",
            "scheme": "http",
            "server": ("192.168.1.23", 8787),
            "headers": [],
            "path": "/xiaozhi/ota/",
            "query_string": b"",
        }
    )

    assert resolve_bridge_host(request) == "192.168.1.23"


def test_load_dotenv_does_not_override_existing_environment(tmp_path: Path, monkeypatch: pytest.MonkeyPatch):
    env_dir = tmp_path / "bridge"
    env_dir.mkdir()
    (env_dir / ".env").write_text("STACKCHAN_BRIDGE_HOST=10.0.0.8\nSTACKCHAN_LLM_MODEL=test-model\n", encoding="utf-8")
    monkeypatch.setenv("STACKCHAN_LLM_MODEL", "preset-model")
    monkeypatch.delenv("STACKCHAN_BRIDGE_HOST", raising=False)

    load_dotenv(env_dir)

    assert os.environ["STACKCHAN_BRIDGE_HOST"] == "10.0.0.8"
    assert os.environ["STACKCHAN_LLM_MODEL"] == "preset-model"
