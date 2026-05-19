/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include <mooncake_log.h>
#include <hal/hal.h>

extern "C" void app_main(void)
{
    // Setup logger
    mclog::set_level(mclog::level_info);
    mclog::set_time_format(mclog::time_format_unix_milliseconds);

    // HAL init
    GetHAL().init();

    // Boot directly into the hands-free xiaozhi runtime instead of waiting
    // for the launcher to open AI.AGENT manually.
    GetHAL().startXiaozhi();
}
