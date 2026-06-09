/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "hal.h"

#include <driver/gpio.h>

#ifndef IR_SEND_GPIO
#define IR_SEND_GPIO GPIO_NUM_5
#endif
#include <mooncake_log.h>
#include <mcp_server.h>
#include <stackchan/stackchan.h>
#include <apps/common/common.h>
#include <charconv>
#include <string_view>

using namespace stackchan;

static const std::string_view _tag = "HAL-MCP";

static bool parse_ir_timings(std::string_view text, std::vector<uint32_t>& timings)
{
    timings.clear();
    while (!text.empty()) {
        size_t comma = text.find(',');
        std::string_view token = comma == std::string_view::npos ? text : text.substr(0, comma);
        while (!token.empty() && token.front() == ' ') {
            token.remove_prefix(1);
        }
        while (!token.empty() && token.back() == ' ') {
            token.remove_suffix(1);
        }
        if (token.empty()) {
            return false;
        }

        uint32_t value = 0;
        auto result    = std::from_chars(token.data(), token.data() + token.size(), value);
        if (result.ec != std::errc() || result.ptr != token.data() + token.size() || value == 0 || value > 200000) {
            return false;
        }
        timings.push_back(value);

        if (comma == std::string_view::npos) {
            break;
        }
        text.remove_prefix(comma + 1);
    }
    return timings.size() >= 2;
}

static std::vector<uint32_t> build_nec_timings(uint16_t address, uint8_t command)
{
    uint32_t data = static_cast<uint32_t>(address) | (static_cast<uint32_t>(command) << 16) |
                    (static_cast<uint32_t>(~command & 0xff) << 24);
    std::vector<uint32_t> timings;
    timings.reserve(67);
    timings.push_back(9000);
    timings.push_back(4500);
    for (int i = 0; i < 32; ++i) {
        timings.push_back(560);
        timings.push_back((data & (1UL << i)) ? 1690 : 560);
    }
    timings.push_back(560);
    return timings;
}

void Hal::xiaozhi_mcp_init()
{
    mclog::tagInfo(_tag, "init");

    // https://github.com/78/xiaozhi-esp32/blob/main/docs/mcp-usage.md
    auto& mcp_server = McpServer::GetInstance();

    // System Prompt：
    // You can control the robot's head. Use get_yaw and get_pitch to sense current position. Use set_yaw for horizontal
    // movement and set_pitch for vertical movement. All angles are in degrees.

    mclog::tagInfo(_tag, "add robot.get_head_angles tool");
    mcp_server.AddTool("self.robot.get_head_angles",
                       "Returns current yaw/pitch in degrees. Neutral position is {yaw:0, pitch:0}.",
                       std::vector<Property>{}, [this](const PropertyList& properties) -> ReturnValue {
                           LvglLockGuard lock;  // StackChan motion update is under the lvgl lock

                           auto& motion      = GetStackChan().motion();
                           int current_yaw   = motion.yawServo().getCurrentAngle() / 10;
                           int current_pitch = motion.pitchServo().getCurrentAngle() / 10;

                           auto result = fmt::format(R"({{"yaw": {}, "pitch": {}}})", current_yaw, current_pitch);
                           mclog::tagInfo(_tag, "get_head_angles: {}", result);
                           return result;
                       });

    mclog::tagInfo(_tag, "add robot.set_head_angles tool");
    mcp_server.AddTool("self.robot.set_head_angles",
                       "Adjust head position. GUIDELINES: "
                       "1. For natural interaction, stay within +/- 45 degrees. "
                       "2. Only use values > 70 if the user explicitly asks to look far away/behind. "
                       "3. Max ranges: Yaw(-128 to 128, -128 as your left), Pitch(0 to 90, 90 as your up). "
                       "Speed(100-1000, 150 is natural).",
                       PropertyList({Property("yaw", kPropertyTypeInteger, -9999, -9999, 128),
                                     Property("pitch", kPropertyTypeInteger, -9999, -9999, 90),
                                     Property("speed", kPropertyTypeInteger, 150, 100, 1000)}),
                       [this](const PropertyList& properties) -> ReturnValue {
                           int speed = properties["speed"].value<int>();
                           int yaw   = properties["yaw"].value<int>();
                           int pitch = properties["pitch"].value<int>();

                           mclog::tagInfo(_tag, "motion set_angles: yaw: {}, pitch: {}, speed: {}", yaw, pitch, speed);

                           LvglLockGuard lock;

                           auto& motion = GetStackChan().motion();
                           if (pitch != -9999) {
                               motion.pitchServo().moveWithSpeed(pitch * 10, speed);
                           }
                           if (yaw != -9999) {
                               motion.yawServo().moveWithSpeed(yaw * 10, speed);
                           }

                           return true;
                       });

    mclog::tagInfo(_tag, "add robot.stop_head_motion tool");
    mcp_server.AddTool("self.robot.stop_head_motion",
                       "Stop StackChan head movement during IR debugging by locking motion modifiers and holding the current angle.",
                       std::vector<Property>{}, [this](const PropertyList& properties) -> ReturnValue {
                           LvglLockGuard lock;

                           auto& motion = GetStackChan().motion();
                           motion.setModifyLock(true);
                           motion.setAutoAngleSyncEnabled(false);
                           const int yaw   = motion.yawServo().getCurrentAngle();
                           const int pitch = motion.pitchServo().getCurrentAngle();
                           motion.yawServo().moveWithSpeed(yaw, 1000);
                           motion.pitchServo().moveWithSpeed(pitch, 1000);

                           auto result = fmt::format(R"({{"stopped":true,"yaw":{},"pitch":{}}})", yaw / 10, pitch / 10);
                           mclog::tagInfo(_tag, "stop_head_motion: {}", result);
                           return result;
                       });

    mclog::tagInfo(_tag, "add robot.set_servo_power_enabled tool");
    mcp_server.AddTool("self.robot.set_servo_power_enabled",
                       "Enable or disable StackChan servo power. Disable this during IR debugging when head movement changes the IR LED direction.",
                       PropertyList({Property("enabled", kPropertyTypeBoolean, true)}),
                       [this](const PropertyList& properties) -> ReturnValue {
                           const bool enabled = properties["enabled"].value<bool>();
                           LvglLockGuard lock;
                           auto& motion = GetStackChan().motion();
                           motion.setModifyLock(!enabled);
                           motion.setAutoAngleSyncEnabled(enabled);
                           GetHAL().setServoPowerEnabled(enabled);

                           auto result = fmt::format(R"({{"servo_power_enabled":{}}})", enabled ? "true" : "false");
                           mclog::tagInfo(_tag, "set_servo_power_enabled: {}", result);
                           return result;
                       });

    mclog::tagInfo(_tag, "add robot.set_led_color tool");
    mcp_server.AddTool(
        "self.robot.set_led_color",
        "Set the color of the robot's INTERNAL onboard LED. This is NOT for room lights. "
        "Values: 0-168 (safe range). Red=168,0,0; Green=0,168,0; Blue=0,0,168; White=100,100,100; Off=0,0,0.",
        PropertyList({Property("red", kPropertyTypeInteger, 0, 0, 168),
                      Property("green", kPropertyTypeInteger, 0, 0, 168),
                      Property("blue", kPropertyTypeInteger, 0, 0, 168)}),
        [this](const PropertyList& properties) -> ReturnValue {
            int r = properties["red"].value<int>();
            int g = properties["green"].value<int>();
            int b = properties["blue"].value<int>();

            mclog::tagInfo(_tag, "set_led_color: r={}, g={}, b={}", r, g, b);

            LvglLockGuard lock;

            GetStackChan().leftNeonLight().setColor(r, g, b);
            GetStackChan().rightNeonLight().setColor(r, g, b);

            return true;
        });

    mclog::tagInfo(_tag, "add robot.create_reminder tool");
    mcp_server.AddTool("self.robot.create_reminder",
                       "Create a reminder. Duration is in seconds. Message is what to say when time is up. Set repeat "
                       "to true to repeat the reminder.",
                       PropertyList({Property("duration_seconds", kPropertyTypeInteger, 60, 1, 86400),
                                     Property("message", kPropertyTypeString, std::string("Time's up!")),
                                     Property("repeat", kPropertyTypeBoolean, false)}),
                       [this](const PropertyList& properties) -> ReturnValue {
                           int duration_seconds = properties["duration_seconds"].value<int>();
                           std::string message  = properties["message"].value<std::string>();
                           bool repeat          = properties["repeat"].value<bool>();

                           // Default message
                           if (message.empty()) {
                               message = "Time's up!";
                           }

                           mclog::tagInfo(_tag, "create_reminder: duration={}s, message={}, repeat={}",
                                          duration_seconds, message, repeat);

                           int id = tools::create_reminder(duration_seconds * 1000, message, repeat);

                           return id;
                       });

    mclog::tagInfo(_tag, "add robot.get_reminders tool");
    mcp_server.AddTool("self.robot.get_reminders", "Get list of active reminders.", std::vector<Property>{},
                       [this](const PropertyList& properties) -> ReturnValue {
                           mclog::tagInfo(_tag, "get_reminders");
                           auto reminders          = tools::get_active_reminders();
                           std::string result_json = "[";
                           for (size_t i = 0; i < reminders.size(); ++i) {
                               const auto& r = reminders[i];
                               result_json +=
                                   fmt::format(R"({{"id": {}, "duration_ms": {}, "message": "{}", "repeat": {}}})",
                                               r.id, r.durationMs, r.message, r.repeat ? "true" : "false");
                               if (i < reminders.size() - 1) {
                                   result_json += ", ";
                               }
                           }
                           result_json += "]";
                           mclog::tagInfo(_tag, "get_reminders result: {}", result_json);
                           return result_json;
                       });

    mclog::tagInfo(_tag, "add robot.stop_reminder tool");
    mcp_server.AddTool("self.robot.stop_reminder", "Stop a reminder by ID.",
                       PropertyList({Property("id", kPropertyTypeInteger, -1)}),
                       [this](const PropertyList& properties) -> ReturnValue {
                           int id = properties["id"].value<int>();
                           mclog::tagInfo(_tag, "stop_reminder: id={}", id);
                           tools::stop_reminder(id);
                           return true;
                       });

    mclog::tagInfo(_tag, "add robot.send_ir_raw tool");
    mcp_server.AddTool(
        "self.robot.send_ir_raw",
        "Send a raw infrared frame from StackChan's built-in IR LED using the ESP-IDF RMT carrier path. "
        "timings_usec is comma-separated mark/space durations in microseconds, starting with a mark.",
        PropertyList({Property("timings_usec", kPropertyTypeString, std::string()),
                      Property("carrier_hz", kPropertyTypeInteger, 38000, 30000, 60000)}),
        [this](const PropertyList& properties) -> ReturnValue {
            std::string timings_text = properties["timings_usec"].value<std::string>();
            int carrier_hz           = properties["carrier_hz"].value<int>();

            std::vector<uint32_t> timings;
            if (!parse_ir_timings(timings_text, timings)) {
                mclog::tagError(_tag, "send_ir_raw invalid timings");
                return false;
            }
            if (!GetHAL().sendIrRaw(timings, static_cast<uint32_t>(carrier_hz))) {
                return false;
            }
            std::string result = "{";
            result += "\"sent\":true";
            result += ",\"driver\":\"ESP-IDF RMT\"";
            result += ",\"function\":\"Hal::sendIrRaw\"";
            result += ",\"gpio\":" + std::to_string(static_cast<int>(IR_SEND_GPIO));
            result += ",\"carrier_hz\":" + std::to_string(carrier_hz);
            result += ",\"timing_count\":" + std::to_string(timings.size());
            result += "}";
            return result;
        });

    mclog::tagInfo(_tag, "add robot.pause_ir_receiver tool");
    mcp_server.AddTool("self.robot.pause_ir_receiver",
                       "Temporarily disable the IR receiver before transmitting.",
                       PropertyList(), [](const PropertyList&) -> ReturnValue {
                           return GetHAL().pauseIrReceiver();
                       });

    mclog::tagInfo(_tag, "add robot.resume_ir_receiver tool");
    mcp_server.AddTool("self.robot.resume_ir_receiver",
                       "Re-enable the IR receiver after transmitting.",
                       PropertyList(), [](const PropertyList&) -> ReturnValue {
                           return GetHAL().resumeIrReceiver();
                       });

    mclog::tagInfo(_tag, "add robot.send_ir_raw_rmt tool");
    mcp_server.AddTool(
        "self.robot.send_ir_raw_rmt",
	        "Send a raw infrared frame using the ESP-IDF RMT carrier path.",
	        PropertyList({Property("timings_usec", kPropertyTypeString, std::string()),
	                      Property("carrier_hz", kPropertyTypeInteger, 38000, 30000, 60000)}),
	        [this](const PropertyList& properties) -> ReturnValue {
	            std::string timings_text = properties["timings_usec"].value<std::string>();
	            int carrier_hz           = properties["carrier_hz"].value<int>();

	            std::vector<uint32_t> timings;
	            if (!parse_ir_timings(timings_text, timings)) {
	                mclog::tagError(_tag, "send_ir_raw_rmt invalid timings");
	                return false;
	            }
	            if (!GetHAL().sendIrRawRmt(timings, static_cast<uint32_t>(carrier_hz))) {
	                return false;
	            }
	            std::string result = "{";
	            result += "\"sent\":true";
	            result += ",\"driver\":\"ESP-IDF RMT\"";
	            result += ",\"function\":\"Hal::sendIrRawRmt\"";
	            result += ",\"gpio\":" + std::to_string(static_cast<int>(IR_SEND_GPIO));
	            result += ",\"carrier_hz\":" + std::to_string(carrier_hz);
	            result += ",\"timing_count\":" + std::to_string(timings.size());
	            result += "}";
            return result;
        });

    mclog::tagInfo(_tag, "add robot.send_ir_raw_rmt_inverted tool");
    mcp_server.AddTool(
        "self.robot.send_ir_raw_rmt_inverted",
        "Send a raw infrared frame using ESP-IDF RMT with inverted mark/space levels.",
        PropertyList({Property("timings_usec", kPropertyTypeString, std::string()),
                      Property("carrier_hz", kPropertyTypeInteger, 38000, 30000, 60000)}),
        [this](const PropertyList& properties) -> ReturnValue {
            std::string timings_text = properties["timings_usec"].value<std::string>();
            int carrier_hz           = properties["carrier_hz"].value<int>();

            std::vector<uint32_t> timings;
            if (!parse_ir_timings(timings_text, timings)) {
                mclog::tagError(_tag, "send_ir_raw_rmt_inverted invalid timings");
                return false;
            }
            if (!GetHAL().sendIrRawRmtInverted(timings, static_cast<uint32_t>(carrier_hz))) {
                return false;
            }
            std::string result = "{";
            result += "\"sent\":true";
            result += ",\"driver\":\"ESP-IDF RMT\"";
            result += ",\"function\":\"Hal::sendIrRawRmtInverted\"";
            result += ",\"gpio\":" + std::to_string(static_cast<int>(IR_SEND_GPIO));
            result += ",\"carrier_hz\":" + std::to_string(carrier_hz);
            result += ",\"timing_count\":" + std::to_string(timings.size());
            result += "}";
            return result;
        });

	    mclog::tagInfo(_tag, "add robot.send_ir_nec_test tool");
    mcp_server.AddTool("self.robot.send_ir_nec_test",
                       "Send a short NEC-format IR test frame from StackChan's built-in IR LED. This is for verifying "
                       "the IR transmitter path before using longer air-conditioner raw frames.",
                       PropertyList({Property("address", kPropertyTypeInteger, 0, 0, 65535),
                                     Property("command", kPropertyTypeInteger, 85, 0, 255)}),
                       [this](const PropertyList& properties) -> ReturnValue {
                           int address = properties["address"].value<int>();
                           int command = properties["command"].value<int>();
                           auto timings =
                               build_nec_timings(static_cast<uint16_t>(address), static_cast<uint8_t>(command));
                           return GetHAL().sendIrRaw(timings, 38000);
                       });

    mclog::tagInfo(_tag, "add robot.test_ir_gpio_blink tool");
    mcp_server.AddTool("self.robot.test_ir_gpio_blink",
                       "Directly blink StackChan's IR LED GPIO without RMT carrier. Use a phone camera to verify "
                       "whether the IR LED physically emits light.",
                       PropertyList({Property("active_low", kPropertyTypeBoolean, false),
                                     Property("pulses", kPropertyTypeInteger, 10, 1, 50)}),
                       [this](const PropertyList& properties) -> ReturnValue {
                           bool active_low = properties["active_low"].value<bool>();
                           int pulses      = properties["pulses"].value<int>();
                           return GetHAL().testIrGpioBlink(active_low, pulses, 160, 160);
                       });

    mclog::tagInfo(_tag, "add robot.get_ir_rx_status tool");
    mcp_server.AddTool("self.robot.get_ir_rx_status",
                       "Return diagnostics for the external IR receiver handled by ESP-IDF RMT: GPIO level, "
                       "decoded frame count, and last decoded frame timing.",
                       std::vector<Property>{}, [this](const PropertyList& properties) -> ReturnValue {
                           return GetHAL().getIrReceiverStatus();
                       });

    mclog::tagInfo(_tag, "add robot.get_ir_rx_latest tool");
    mcp_server.AddTool("self.robot.get_ir_rx_latest",
                       "Return the latest raw infrared frame captured on StackChan by ESP-IDF RMT.",
                       std::vector<Property>{}, [this](const PropertyList& properties) -> ReturnValue {
                           return GetHAL().getIrReceiverLatestRaw();
                       });

    mclog::tagInfo(_tag, "add robot.reset_ir_receiver tool");
    mcp_server.AddTool("self.robot.reset_ir_receiver",
                       "Synchronously clear and re-arm StackChan's external IR receiver without rebooting.",
                       std::vector<Property>{}, [this](const PropertyList& properties) -> ReturnValue {
                           return GetHAL().resetIrReceiver();
                       });
}
