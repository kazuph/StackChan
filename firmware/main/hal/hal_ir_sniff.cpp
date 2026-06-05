/*
 * SPDX-FileCopyrightText: 2026 M5Stack Technology CO LTD
 *
 * SPDX-License-Identifier: MIT
 */
#include "hal.h"
#include "board/config.h"
#include "stackchan_irremote_adapter.h"

#ifndef IR_RECV_GPIO
#define IR_RECV_GPIO GPIO_NUM_10
#endif

void Hal::startIrSniff()
{
    stackchan_irremote_start(static_cast<uint16_t>(IR_RECV_GPIO));
}

bool Hal::resetIrReceiver()
{
    return stackchan_irremote_reset();
}

std::string Hal::getIrReceiverStatus()
{
    return stackchan_irremote_status_json();
}

std::string Hal::getIrReceiverLatestRaw()
{
    return stackchan_irremote_latest_json();
}
