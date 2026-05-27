/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "hal.h"
#include "board/config.h"

#include <driver/gpio.h>
#include <esp_timer.h>
#include <mooncake_log.h>

#include <freertos/FreeRTOS.h>
#include <freertos/queue.h>
#include <freertos/task.h>

#include <string>

static const std::string_view _tag = "HAL-IR-SNIFF";

#ifndef IR_RECV_GPIO
#define IR_RECV_GPIO GPIO_NUM_10
#endif

namespace {

struct IrEdgeEvent {
    uint8_t level;
    uint32_t duration_usec;
};

static QueueHandle_t ir_edge_queue = nullptr;
static int64_t last_edge_time_usec = 0;
static uint8_t last_level          = 1;
static bool have_last_edge         = false;

static void IRAM_ATTR ir_gpio_isr(void* arg)
{
    const int64_t now = esp_timer_get_time();
    const uint8_t current_level = static_cast<uint8_t>(gpio_get_level(IR_RECV_GPIO));

    if (have_last_edge) {
        const int64_t elapsed = now - last_edge_time_usec;
        if (elapsed > 0 && elapsed <= 200000 && ir_edge_queue != nullptr) {
            IrEdgeEvent event = {
                .level         = last_level,
                .duration_usec = static_cast<uint32_t>(elapsed),
            };
            BaseType_t task_woken = pdFALSE;
            xQueueSendFromISR(ir_edge_queue, &event, &task_woken);
            if (task_woken == pdTRUE) {
                portYIELD_FROM_ISR();
            }
        }
    }

    last_edge_time_usec = now;
    last_level          = current_level;
    have_last_edge      = true;
}

static void append_duration(std::string& out, bool& first, uint32_t duration)
{
    if (duration == 0) {
        return;
    }
    if (!first) {
        out.push_back(',');
    }
    first = false;
    out += std::to_string(duration);
}

static void append_level_duration(std::string& out, bool& first, uint8_t level, uint32_t duration)
{
    if (duration == 0) {
        return;
    }
    if (!first) {
        out.push_back(',');
    }
    first = false;
    out += std::to_string(level);
    out.push_back(':');
    out += std::to_string(duration);
}

static void flush_frame(const std::string& levels, const std::string& raw, size_t duration_count)
{
    if (duration_count < 8) {
        return;
    }
    mclog::tagInfo(_tag, "frame durations={}", duration_count);
    printf("IR-SNIFF durations=%u levels=%s raw_usec=%s\n",
           static_cast<unsigned>(duration_count),
           levels.c_str(),
           raw.c_str());
}

static void ir_sniff_task(void* param)
{
    ir_edge_queue = xQueueCreate(2048, sizeof(IrEdgeEvent));
    if (ir_edge_queue == nullptr) {
        mclog::tagError(_tag, "failed to create queue");
        vTaskDelete(nullptr);
        return;
    }

    gpio_config_t config = {};
    config.pin_bit_mask  = 1ULL << IR_RECV_GPIO;
    config.mode          = GPIO_MODE_INPUT;
    config.pull_up_en    = GPIO_PULLUP_ENABLE;
    config.pull_down_en  = GPIO_PULLDOWN_DISABLE;
    config.intr_type     = GPIO_INTR_ANYEDGE;
    esp_err_t err        = gpio_config(&config);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "gpio_config failed: {}", esp_err_to_name(err));
        vTaskDelete(nullptr);
        return;
    }

    err = gpio_install_isr_service(ESP_INTR_FLAG_IRAM);
    if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) {
        mclog::tagError(_tag, "gpio_install_isr_service failed: {}", esp_err_to_name(err));
        vTaskDelete(nullptr);
        return;
    }

    err = gpio_isr_handler_add(IR_RECV_GPIO, ir_gpio_isr, nullptr);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "gpio_isr_handler_add failed: {}", esp_err_to_name(err));
        vTaskDelete(nullptr);
        return;
    }

    mclog::tagInfo(_tag, "sniff mode started on gpio {}", static_cast<int>(IR_RECV_GPIO));

    std::string raw;
    std::string levels;
    raw.reserve(4096);
    levels.reserve(4096);
    bool first_raw    = true;
    bool first_levels = true;
    size_t duration_count = 0;

    while (true) {
        IrEdgeEvent event = {};
        if (xQueueReceive(ir_edge_queue, &event, pdMS_TO_TICKS(140)) == pdTRUE) {
            append_duration(raw, first_raw, event.duration_usec);
            append_level_duration(levels, first_levels, event.level, event.duration_usec);
            ++duration_count;
            continue;
        }

        if (duration_count > 0) {
            flush_frame(levels, raw, duration_count);
            raw.clear();
            levels.clear();
            first_raw      = true;
            first_levels   = true;
            duration_count = 0;
        }
    }
}

} // namespace

void Hal::startIrSniff()
{
    static bool started = false;
    if (started) {
        return;
    }
    started = true;
    xTaskCreatePinnedToCore(ir_sniff_task, "ir_sniff", 8192, nullptr, 4, nullptr, 1);
}
