/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "hal.h"
#include "board/config.h"

#include <driver/gpio.h>
#include <driver/rmt_tx.h>
#include <esp_check.h>
#include <freertos/FreeRTOS.h>
#include <freertos/task.h>
#include <mooncake_log.h>

#include <algorithm>
#include <utility>

static const std::string_view _tag = "HAL-IR";
static constexpr uint16_t kMaxRmtDurationUsec = 32767;

#ifndef IR_SEND_GPIO
#define IR_SEND_GPIO GPIO_NUM_5
#endif

static bool sendIrRawRmtWithPolarity(const std::vector<uint32_t>& timingsUsec, uint32_t carrierHz, bool inverted);

bool Hal::testIrGpioBlink(bool activeLow, int pulses, int onMs, int offMs)
{
    pulses = std::clamp(pulses, 1, 50);
    onMs   = std::clamp(onMs, 10, 1000);
    offMs  = std::clamp(offMs, 10, 1000);

    gpio_config_t config = {};
    config.pin_bit_mask  = 1ULL << IR_SEND_GPIO;
    config.mode          = GPIO_MODE_OUTPUT;
    config.pull_up_en    = GPIO_PULLUP_DISABLE;
    config.pull_down_en  = GPIO_PULLDOWN_DISABLE;
    config.intr_type     = GPIO_INTR_DISABLE;

    esp_err_t err = gpio_config(&config);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "gpio_config failed: {}", esp_err_to_name(err));
        return false;
    }

    const int onLevel  = activeLow ? 0 : 1;
    const int offLevel = activeLow ? 1 : 0;
    gpio_set_level(IR_SEND_GPIO, offLevel);

    for (int i = 0; i < pulses; ++i) {
        gpio_set_level(IR_SEND_GPIO, onLevel);
        vTaskDelay(pdMS_TO_TICKS(onMs));
        gpio_set_level(IR_SEND_GPIO, offLevel);
        vTaskDelay(pdMS_TO_TICKS(offMs));
    }

    mclog::tagInfo(_tag, "blinked ir gpio: gpio={}, active_low={}, pulses={}",
                   static_cast<int>(IR_SEND_GPIO), activeLow, pulses);
    return true;
}

bool Hal::sendIrRaw(const std::vector<uint32_t>& timingsUsec, uint32_t carrierHz)
{
    return sendIrRawRmt(timingsUsec, carrierHz);
}

bool Hal::sendIrRawRmt(const std::vector<uint32_t>& timingsUsec, uint32_t carrierHz)
{
    return sendIrRawRmtWithPolarity(timingsUsec, carrierHz, false);
}

bool Hal::sendIrRawRmtInverted(const std::vector<uint32_t>& timingsUsec, uint32_t carrierHz)
{
    return sendIrRawRmtWithPolarity(timingsUsec, carrierHz, true);
}

static bool sendIrRawRmtWithPolarity(const std::vector<uint32_t>& timingsUsec, uint32_t carrierHz, bool inverted)
{
    if (timingsUsec.size() < 2 || timingsUsec.size() > 1200) {
        mclog::tagError(_tag, "invalid raw timing count for RMT: {}", timingsUsec.size());
        return false;
    }

    rmt_channel_handle_t tx_channel = nullptr;
    rmt_encoder_handle_t copy_encoder = nullptr;
    auto cleanup = [&]() {
        if (tx_channel != nullptr) {
            rmt_disable(tx_channel);
        }
        if (copy_encoder != nullptr) {
            rmt_del_encoder(copy_encoder);
        }
        if (tx_channel != nullptr) {
            rmt_del_channel(tx_channel);
        }
        gpio_set_level(IR_SEND_GPIO, inverted ? 1 : 0);
    };

    rmt_tx_channel_config_t tx_config = {};
    tx_config.gpio_num                  = IR_SEND_GPIO;
    tx_config.clk_src                   = RMT_CLK_SRC_DEFAULT;
    tx_config.resolution_hz             = 1000000;
    tx_config.mem_block_symbols         = 128;
    tx_config.trans_queue_depth         = 4;

    esp_err_t err = rmt_new_tx_channel(&tx_config, &tx_channel);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "rmt_new_tx_channel failed: {}", esp_err_to_name(err));
        return false;
    }

    rmt_carrier_config_t carrier_config = {};
    carrier_config.frequency_hz         = carrierHz;
    carrier_config.duty_cycle           = 0.33f;
    carrier_config.flags.polarity_active_low = false;
    err = rmt_apply_carrier(tx_channel, &carrier_config);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "rmt_apply_carrier failed: {}", esp_err_to_name(err));
        cleanup();
        return false;
    }

    rmt_copy_encoder_config_t encoder_config = {};
    err                                      = rmt_new_copy_encoder(&encoder_config, &copy_encoder);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "rmt_new_copy_encoder failed: {}", esp_err_to_name(err));
        cleanup();
        return false;
    }

    err = rmt_enable(tx_channel);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "rmt_enable failed: {}", esp_err_to_name(err));
        cleanup();
        return false;
    }

    std::vector<std::pair<uint16_t, uint8_t>> durations;
    durations.reserve(timingsUsec.size());
    for (size_t i = 0; i < timingsUsec.size(); ++i) {
        uint32_t remaining = timingsUsec[i];
        const uint8_t level = (i % 2 == 0) ? (inverted ? 0 : 1) : (inverted ? 1 : 0);
        while (remaining > 0) {
            const uint16_t chunk = static_cast<uint16_t>(std::min<uint32_t>(remaining, kMaxRmtDurationUsec));
            if (!durations.empty() && durations.back().second == level &&
                static_cast<uint32_t>(durations.back().first) + chunk <= kMaxRmtDurationUsec) {
                durations.back().first = static_cast<uint16_t>(durations.back().first + chunk);
            } else {
                durations.push_back({chunk, level});
            }
            remaining -= chunk;
        }
    }

    std::vector<rmt_symbol_word_t> symbols;
    symbols.reserve((durations.size() + 1) / 2);
    for (size_t i = 0; i < durations.size(); i += 2) {
        rmt_symbol_word_t symbol = {};
        symbol.duration0         = durations[i].first;
        symbol.level0            = durations[i].second;
        symbol.duration1         = (i + 1 < durations.size()) ? durations[i + 1].first : 1;
        symbol.level1            = (i + 1 < durations.size()) ? durations[i + 1].second : 0;
        symbols.push_back(symbol);
    }

    rmt_transmit_config_t transmit_config = {};
    err = rmt_transmit(tx_channel, copy_encoder, symbols.data(),
                       symbols.size() * sizeof(rmt_symbol_word_t), &transmit_config);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "rmt_transmit failed: {}", esp_err_to_name(err));
        cleanup();
        return false;
    }

    err = rmt_tx_wait_all_done(tx_channel, 5000);
    if (err != ESP_OK) {
        mclog::tagError(_tag, "rmt_tx_wait_all_done failed: {}", esp_err_to_name(err));
        cleanup();
        return false;
    }

    mclog::tagInfo(_tag, "sent raw ir via RMT: count={}, carrier={}Hz, inverted={}", timingsUsec.size(), carrierHz, inverted);
    cleanup();
    return true;
}
