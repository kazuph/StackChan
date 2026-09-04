# StackChan Open-Source

<img src="https://m5stack-doc.oss-cn-shenzhen.aliyuncs.com/1205/K151_stack_chan_main_pictures_01.webp" width="60%">

Here are StackChan related open-source resources, including source code of the StackChan firmware, remote controller firmware, mobile app (iOS and Android), and server. 

Update of this repo could be a little late than the released firmware and mobile app. 

----

<img src="https://cdn.shopify.com/s/files/1/0056/7689/2250/files/5a589623895f65487717894d9240f6b8.png" width="60%">

**StackChan is a super kawaii AI desktop robot co-created by M5Stack and the user community.** It uses the M5Stack **flagship IoT development kit [CoreS3](https://docs.m5stack.com/en/core/CoreS3)** as its main controller, powered by an ESP32-S3 SoC featuring a 240 MHz dual-core processor, with 16MB Flash and 8MB PSRAM onboard, and supporting Wi-Fi and BLE. The main unit also integrates a 2.0-inch capacitive touch display with a high-strength glass cover, a 0.3 MP camera, a proximity & ambient light sensor, a 9-axis IMU (accelerometer + gyroscope + magnetometer), a microSD card slot, a 1W speaker, dual microphones, and power/reset buttons. 

The **robot body**, connected to the main unit, includes a USB-C interface for power and data, a 550 mAh battery, two feedback servos (360-degree continuous rotation on the horizontal axis and 90-degree movement on the vertical axis), two rows totaling 12 RGB LEDs, infrared transmitter and receiver, a three-zone touch panel, and a full-featured NFC module. 

The **factory firmware** is feature-rich, including an AI Agent, lively and expressive animations, ESP-NOW wireless remote control, and online app downloads. It can connect to a mobile app for video viewing, remote avatar control, and more, and also supports online updates (OTA). The product also supports programming via Arduino, UiFlow2, and other methods, and can connect to various expansion units in the M5Stack ecosystem, making it easy to implement a wide range of custom functions. 

> ⚠️ Do not forcibly rotate any movable parts connected to the motors by hand when you are unsure whether the motors are powered and under control, as this may cause hardware damage. 

- Purchase link: [M5Stack Official Store](https://shop.m5stack.com/products/stackchan-kawaii-co-created-open-source-ai-desktop-robot) | [淘宝 Taobao](https://item.taobao.com/item.htm?id=1042238294510)

- Product document page: [English](https://docs.m5stack.com/en/StackChan) | [日本語](https://docs.m5stack.com/ja/StackChan) | [中文](https://docs.m5stack.com/zh_CN/StackChan)

- Board support package: https://github.com/m5stack/StackChan-BSP

## Current Development Direction

This repository is currently being used for a custom StackChan direction rather than strict compatibility with the stock M5Stack firmware, app, and cloud services.

The current goals are:

- Hands-free conversation using STT, local LLM, and TTS processes running on an SSH-accessible PC
- Integration with a Hermes agent running on that PC so StackChan can use agent capabilities through StackChan, and Hermes can also control StackChan bidirectionally
- Remote audio/video calling from a React Native app through StackChan

In this direction, StackChan is treated more like a network-connected device endpoint for audio, camera, motion, expression, and speech control, while the main intelligence and orchestration live on the PC/agent side.

## Voice bridge environment setup

The custom voice bridge now runs from the Go implementation under `server/bridgego/`. It reads StackChan-specific runtime settings from the existing local `.env` file under `server/bridge/`. This keeps machine-specific LAN IPs, local API URLs, and voice settings out of tracked source.

The committed file is `server/bridge/.env.example`. The actual runtime file is `server/bridge/.env` (or repository-root `.env`), and it is intentionally **not committed**.

Create the local file before running the bridge:

```bash
cp server/bridge/.env.example server/bridge/.env
```

Then edit `server/bridge/.env` for your machine:

```dotenv
STACKCHAN_BRIDGE_HOST=192.168.x.x
STACKCHAN_BRIDGE_PORT=8787
STACKCHAN_STT_URL=http://127.0.0.1:8088/api/stt/v1/stt
STACKCHAN_TTS_URL=http://127.0.0.1:8088/api/tts/v1/tts
STACKCHAN_LLM_URL=http://127.0.0.1:8088/api/llm/v1/chat/completions
STACKCHAN_LLM_MODEL=
STACKCHAN_LLM_API_KEY=
STACKCHAN_LLM_MAX_TOKENS=2048
STACKCHAN_GEMINI_FALLBACK_URL=https://generativelanguage.googleapis.com/v1beta/openai/chat/completions
STACKCHAN_GEMINI_FALLBACK_MODEL=gemini-2.5-flash-lite
STACKCHAN_GEMINI_API_KEY=
STACKCHAN_VOICE_LOCK_ID=
STACKCHAN_ENABLE_TV_VOICE_CONTROL=false
```

`STACKCHAN_ENABLE_TV_VOICE_CONTROL=true` enables continuous edge-VAD listening. Only detected speech is sent to STT, and only the fixed LG TV power, volume, and channel 1–3 phrases are dispatched; all other transcripts are ignored without LLM or TTS.

Run the Go bridge:

```bash
cd server
go run ./cmd/stackchan-voice-bridge
```

For compatibility, the old direct Python entrypoint also delegates to the Go bridge:

```bash
python server/bridge/stackchan_voice_bridge.py
```

Set `STACKCHAN_LEGACY_PYTHON_BRIDGE=1` only when you intentionally need to run the old Python bridge.

Notes:

- If conversation memory were gone, the repository still tells you the expected file layout: `server/bridge/.env.example` is committed, while `server/bridge/.env` must exist locally and is ignored.
- `STACKCHAN_BRIDGE_HOST` is optional. If omitted, the OTA endpoint uses the request host automatically.
- `STACKCHAN_STT_URL`, `STACKCHAN_TTS_URL`, and `STACKCHAN_LLM_URL` default to `127.0.0.1` so the bridge can run on the same host as the gateway/backends without embedding a personal LAN IP in source control.
- `STACKCHAN_LLM_MODEL` and `STACKCHAN_VOICE_LOCK_ID` are also optional so model choice and voice choice can stay local to each machine.
- `STACKCHAN_LLM_MAX_TOKENS` defaults to `2048` for the main conversation response budget.
- `STACKCHAN_GEMINI_FALLBACK_*` lets the bridge fall back to Gemini Flash-Lite through Google's OpenAI-compatible endpoint when the primary LLM fails.
- The Go bridge still binds on `0.0.0.0:$STACKCHAN_BRIDGE_PORT` when started directly.

Thank you to the contributors of the StackChan community, especially: 

| ![](https://m5stack-doc.oss-cn-shenzhen.aliyuncs.com/1205/avatar_stack_chan.jpg) | ![](https://m5stack-doc.oss-cn-shenzhen.aliyuncs.com/1205/avatar_takao.jpg) |
| -------------------------------------------------------------------------------- | --------------------------------------------------------------------------- |
| [@stack_chan](https://x.com/stack_chan)                                          | [@mongonta555](https://x.com/mongonta555)                                   |
| Shinya Ishikawa                                                                  | Takao Akaki                                                                 |
