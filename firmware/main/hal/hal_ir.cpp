/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "hal.h"
#include "board/config.h"
#include "stackchan_irremote_adapter.h"

#include <driver/gpio.h>
#include <freertos/FreeRTOS.h>
#include <freertos/task.h>
#include <mooncake_log.h>

#include <algorithm>
#include <utility>

static const std::string_view _tag = "HAL-IR";

#ifndef IR_SEND_GPIO
#define IR_SEND_GPIO GPIO_NUM_5
#endif

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
    if (timingsUsec.size() < 2 || timingsUsec.size() > 1200) {
        mclog::tagError(_tag, "invalid raw timing count: {}", timingsUsec.size());
        return false;
    }
    const bool ok = stackchan_irremote_send_raw(
        static_cast<uint16_t>(IR_SEND_GPIO), timingsUsec.data(), timingsUsec.size(), carrierHz);
    if (!ok) {
        mclog::tagError(_tag, "IRremoteESP8266 sendRaw failed: count={}, carrier={}Hz", timingsUsec.size(), carrierHz);
        return false;
    }
    mclog::tagInfo(_tag, "sent raw ir via IRremoteESP8266: count={}, carrier={}Hz", timingsUsec.size(), carrierHz);
    return true;
}
