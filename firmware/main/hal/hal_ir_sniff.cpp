/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "hal.h"
#include "board/config.h"

#include <driver/gpio.h>
#include <driver/rmt_rx.h>
#include <esp_timer.h>
#include <freertos/FreeRTOS.h>
#include <freertos/queue.h>
#include <freertos/task.h>
#include <mooncake_log.h>

#include <algorithm>
#include <mutex>
#include <string>
#include <vector>

#ifndef IR_RECV_GPIO
#define IR_RECV_GPIO GPIO_NUM_10
#endif

namespace {

static const std::string_view _tag = "HAL-IR-RX";
static constexpr uint32_t kResolutionHz = 1000000;
static constexpr size_t kMaxSymbols = 700;
static constexpr uint32_t kMinPulseUsec = 50;
static constexpr uint32_t kFrameEndUsec = 120000;

rmt_channel_handle_t rx_channel = nullptr;
QueueHandle_t receive_queue = nullptr;
TaskHandle_t receive_task_handle = nullptr;
rmt_symbol_word_t raw_symbols[kMaxSymbols];
std::mutex state_mutex;
bool receiver_started = false;
bool receiver_paused = false;
uint32_t frame_count = 0;
uint32_t receive_count = 0;
uint32_t overflow_frame_count = 0;
uint32_t queue_drop_count = 0;
uint32_t last_duration_count = 0;
uint32_t last_symbol_count = 0;
int64_t last_frame_time_usec = 0;
std::string latest_json = "{\"decoded\":false,\"source\":\"esp-idf-rmt\",\"raw_usec\":\"\"}";
std::string status_json = "{\"source\":\"esp-idf-rmt\",\"receiver_configured\":false}";

std::string json_escape(const std::string& input)
{
    std::string out;
    out.reserve(input.size() + 8);
    for (char ch : input) {
        switch (ch) {
        case '\\':
            out += "\\\\";
            break;
        case '"':
            out += "\\\"";
            break;
        case '\n':
            out += "\\n";
            break;
        case '\r':
            out += "\\r";
            break;
        case '\t':
            out += "\\t";
            break;
        default:
            out.push_back(ch);
            break;
        }
    }
    return out;
}

void update_status_locked()
{
    const int64_t now = esp_timer_get_time();
    const int64_t age_ms = last_frame_time_usec > 0 ? (now - last_frame_time_usec) / 1000 : -1;
    status_json = "{";
    status_json += "\"source\":\"esp-idf-rmt\"";
    status_json += ",\"gpio\":" + std::to_string(static_cast<int>(IR_RECV_GPIO));
    status_json += ",\"level\":" + std::to_string(gpio_get_level(IR_RECV_GPIO));
    status_json += ",\"receiver_configured\":" + std::string(receiver_started ? "true" : "false");
    status_json += ",\"task_running\":" + std::string(receive_task_handle != nullptr ? "true" : "false");
    status_json += ",\"paused\":" + std::string(receiver_paused ? "true" : "false");
    status_json += ",\"frame_count\":" + std::to_string(frame_count);
    status_json += ",\"receive_count\":" + std::to_string(receive_count);
    status_json += ",\"queue_drop_count\":" + std::to_string(queue_drop_count);
    status_json += ",\"overflow_frame_count\":" + std::to_string(overflow_frame_count);
    status_json += ",\"last_frame_age_ms\":" + std::to_string(age_ms);
    status_json += ",\"last_frame_duration_count\":" + std::to_string(last_duration_count);
    status_json += ",\"last_symbol_count\":" + std::to_string(last_symbol_count);
    status_json += ",\"capture_buffer_symbols\":" + std::to_string(kMaxSymbols);
    status_json += ",\"frame_end_usec\":" + std::to_string(kFrameEndUsec);
    status_json += "}";
}

std::string latest_json_with_age_locked()
{
    if (last_frame_time_usec <= 0) {
        return latest_json;
    }
    const int64_t age_ms = (esp_timer_get_time() - last_frame_time_usec) / 1000;
    std::string out = latest_json;
    const auto marker = out.find("\"age_ms\":");
    if (marker == std::string::npos) {
        return out;
    }
    const auto value_start = marker + 9;
    const auto value_end = out.find(',', value_start);
    if (value_end == std::string::npos) {
        return out;
    }
    out.replace(value_start, value_end - value_start, std::to_string(age_ms));
    return out;
}

std::vector<std::pair<uint8_t, uint32_t>> symbols_to_levels(const rmt_symbol_word_t* symbols, size_t symbol_count)
{
    std::vector<std::pair<uint8_t, uint32_t>> levels;
    levels.reserve(symbol_count * 2);
    auto push = [&](uint8_t level, uint32_t duration) {
        if (duration < kMinPulseUsec) {
            return;
        }
        if (!levels.empty() && levels.back().first == level) {
            levels.back().second += duration;
        } else {
            levels.push_back({level, duration});
        }
    };

    for (size_t i = 0; i < symbol_count; ++i) {
        push(symbols[i].level0, symbols[i].duration0);
        push(symbols[i].level1, symbols[i].duration1);
    }
    return levels;
}

std::vector<uint32_t> levels_to_active_low_raw(const std::vector<std::pair<uint8_t, uint32_t>>& levels)
{
    std::vector<uint32_t> timings;
    timings.reserve(levels.size());

    size_t start = 0;
    while (start < levels.size() && levels[start].first != 0) {
        ++start;
    }
    for (size_t i = start; i < levels.size(); ++i) {
        timings.push_back(levels[i].second);
    }

    if (!timings.empty() && levels.back().first == 0) {
        timings.push_back(1);
    }
    return timings;
}

std::string timings_to_csv(const std::vector<uint32_t>& timings)
{
    std::string out;
    out.reserve(timings.size() * 6);
    for (size_t i = 0; i < timings.size(); ++i) {
        if (i > 0) {
            out.push_back(',');
        }
        out += std::to_string(timings[i]);
    }
    return out;
}

void store_frame(const rmt_symbol_word_t* symbols, size_t symbol_count)
{
    const auto levels = symbols_to_levels(symbols, symbol_count);
    const auto timings = levels_to_active_low_raw(levels);
    const std::string raw = timings_to_csv(timings);

    std::lock_guard<std::mutex> lock(state_mutex);
    receive_count++;
    frame_count++;
    last_frame_time_usec = esp_timer_get_time();
    last_duration_count = timings.size();
    last_symbol_count = symbol_count;
    latest_json = "{";
    latest_json += "\"decoded\":true";
    latest_json += ",\"source\":\"esp-idf-rmt\"";
    latest_json += ",\"protocol\":\"RAW\"";
    latest_json += ",\"manufacturer\":\"Unknown\"";
    latest_json += ",\"frame_count\":" + std::to_string(frame_count);
    latest_json += ",\"decode_count\":" + std::to_string(frame_count);
    latest_json += ",\"age_ms\":0";
    latest_json += ",\"bits\":0";
    latest_json += ",\"repeat\":false";
    latest_json += ",\"overflow\":false";
    latest_json += ",\"rawlen\":" + std::to_string(timings.size());
    latest_json += ",\"durations\":" + std::to_string(timings.size());
    latest_json += ",\"symbols\":" + std::to_string(symbol_count);
    latest_json += ",\"raw_usec\":\"" + json_escape(raw) + "\"";
    latest_json += "}";
    update_status_locked();
}

bool on_rmt_receive_done(rmt_channel_handle_t channel, const rmt_rx_done_event_data_t* edata, void* user_data)
{
    BaseType_t high_task_wakeup = pdFALSE;
    auto queue = static_cast<QueueHandle_t>(user_data);
    if (xQueueSendFromISR(queue, edata, &high_task_wakeup) != pdTRUE) {
        queue_drop_count++;
    }
    return high_task_wakeup == pdTRUE;
}

void arm_receive()
{
    if (rx_channel == nullptr || receiver_paused) {
        return;
    }
    rmt_receive_config_t receive_config = {};
    receive_config.signal_range_min_ns = kMinPulseUsec * 1000;
    receive_config.signal_range_max_ns = kFrameEndUsec * 1000;
    esp_err_t err = rmt_receive(rx_channel, raw_symbols, sizeof(raw_symbols), &receive_config);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "rmt_receive failed: {}", esp_err_to_name(err));
    }
}

void receive_task(void*)
{
    rmt_rx_done_event_data_t rx_data = {};
    arm_receive();
    while (true) {
        if (xQueueReceive(receive_queue, &rx_data, pdMS_TO_TICKS(1000)) == pdPASS) {
            if (rx_data.num_symbols >= kMaxSymbols) {
                overflow_frame_count++;
            }
            if (rx_data.num_symbols > 0) {
                store_frame(rx_data.received_symbols, rx_data.num_symbols);
            }
            arm_receive();
        }
    }
}

}  // namespace

void Hal::startIrSniff()
{
    if (receiver_started) {
        return;
    }

    rmt_rx_channel_config_t rx_config = {};
    rx_config.gpio_num = IR_RECV_GPIO;
    rx_config.clk_src = RMT_CLK_SRC_DEFAULT;
    rx_config.resolution_hz = kResolutionHz;
    rx_config.mem_block_symbols = 128;

    esp_err_t err = rmt_new_rx_channel(&rx_config, &rx_channel);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "rmt_new_rx_channel failed: {}", esp_err_to_name(err));
        return;
    }

    receive_queue = xQueueCreate(4, sizeof(rmt_rx_done_event_data_t));
    if (receive_queue == nullptr) {
        mclog::tagError(_tag, "xQueueCreate failed");
        return;
    }

    rmt_rx_event_callbacks_t callbacks = {};
    callbacks.on_recv_done = on_rmt_receive_done;
    err = rmt_rx_register_event_callbacks(rx_channel, &callbacks, receive_queue);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "rmt_rx_register_event_callbacks failed: {}", esp_err_to_name(err));
        return;
    }

    err = rmt_enable(rx_channel);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "rmt_enable rx failed: {}", esp_err_to_name(err));
        return;
    }

    receiver_started = true;
    {
        std::lock_guard<std::mutex> lock(state_mutex);
        update_status_locked();
    }
    xTaskCreate(receive_task, "ir_rmt_rx", 4096, nullptr, 5, &receive_task_handle);
    mclog::tagInfo(_tag, "started RMT IR receiver: gpio={}, max_symbols={}", static_cast<int>(IR_RECV_GPIO), kMaxSymbols);
}

bool Hal::resetIrReceiver()
{
    std::lock_guard<std::mutex> lock(state_mutex);
    frame_count = 0;
    receive_count = 0;
    overflow_frame_count = 0;
    queue_drop_count = 0;
    last_duration_count = 0;
    last_symbol_count = 0;
    last_frame_time_usec = 0;
    latest_json = "{\"decoded\":false,\"source\":\"esp-idf-rmt\",\"raw_usec\":\"\"}";
    update_status_locked();
    return true;
}

bool Hal::pauseIrReceiver()
{
    receiver_paused = true;
    if (rx_channel != nullptr) {
        rmt_disable(rx_channel);
    }
    std::lock_guard<std::mutex> lock(state_mutex);
    update_status_locked();
    return true;
}

bool Hal::resumeIrReceiver()
{
    receiver_paused = false;
    if (rx_channel != nullptr) {
        esp_err_t err = rmt_enable(rx_channel);
        if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) {
            mclog::tagError(_tag, "rmt_enable rx failed: {}", esp_err_to_name(err));
            return false;
        }
        arm_receive();
    }
    std::lock_guard<std::mutex> lock(state_mutex);
    update_status_locked();
    return true;
}

std::string Hal::getIrReceiverStatus()
{
    std::lock_guard<std::mutex> lock(state_mutex);
    update_status_locked();
    return status_json;
}

std::string Hal::getIrReceiverLatestRaw()
{
    std::lock_guard<std::mutex> lock(state_mutex);
    return latest_json_with_age_locked();
}
