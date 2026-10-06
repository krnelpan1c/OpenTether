package org.opentether.app.util

import java.util.Locale

/** Formats a byte count, e.g. "12.4 MB". */
fun formatBytes(bytes: Long): String {
    val units = arrayOf("B", "KB", "MB", "GB", "TB")
    var value = bytes.toDouble()
    var unit = 0
    while (value >= 1024 && unit < units.lastIndex) {
        value /= 1024
        unit++
    }
    return if (unit == 0 || value >= 100) {
        String.format(Locale.getDefault(), "%.0f %s", value, units[unit])
    } else {
        String.format(Locale.getDefault(), "%.1f %s", value, units[unit])
    }
}

/** Formats bytes per second, e.g. "3.2 MB/s". */
fun formatRate(bytesPerSecond: Long): String = formatBytes(bytesPerSecond) + "/s"

/** Splits a rate into number and unit for large display text. */
fun splitRate(bytesPerSecond: Long): Pair<String, String> {
    val text = formatRate(bytesPerSecond)
    val i = text.indexOf(' ')
    return if (i < 0) text to "" else text.substring(0, i) to text.substring(i + 1)
}
