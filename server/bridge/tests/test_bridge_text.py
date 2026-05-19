import asyncio
import os
from pathlib import Path

import httpx
import pytest
from fastapi import Request

from server.bridge import stackchan_voice_bridge as bridge
from server.bridge.stackchan_env import load_dotenv
from server.bridge.stackchan_voice_bridge import (
    is_repetitive_answer,
    is_exit_phrase,
    resolve_bridge_host,
    run_llm,
    run_tts,
    sanitize_llm_text,
    sanitize_startup_greeting,
    summarize_http_error,
)


def test_sanitize_llm_text_strips_think_tags():
    raw = "<think>internal reasoning</think>\n\nこんにちは。"

    assert sanitize_llm_text(raw, "元気?") == "こんにちは。"


def test_sanitize_llm_text_drops_parrot_reply():
    assert sanitize_llm_text("こんにちは", "こんにちは") == ""


def test_sanitize_llm_text_keeps_short_reply():
    raw = "スタックちゃんだよ。よろしくね。3文目は切ってね。"

    assert sanitize_llm_text(raw, "自己紹介して") == "スタックちゃんだよ。よろしくね。"


def test_is_exit_phrase_detects_good_night():
    assert is_exit_phrase("じゃあお休み")


def test_sanitize_startup_greeting_keeps_one_short_sentence():
    raw = "<think>foo</think>ふふふ、起きたよ。今日はいい日だよ。"

    assert sanitize_startup_greeting(raw) == "ふふふ、起きたよ。"


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

    async def fake_post(url, json):
        captured["messages"] = json["messages"]
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


def test_run_tts_retries_transient_gateway_errors(monkeypatch: pytest.MonkeyPatch):
    request = httpx.Request("POST", bridge.TTS_URL)
    responses = [
        httpx.Response(502, request=request, text="bad gateway"),
        httpx.Response(200, request=request, content=b"wav-bytes"),
    ]

    async def fake_post(url, json):
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
