package org.opentether.app.core

import android.content.Context
import androidx.core.content.edit
import java.security.SecureRandom

/** Small persisted settings. */
class Prefs(context: Context) {
    private val sp = context.getSharedPreferences("opentether", Context.MODE_PRIVATE)

    var upstream: Upstream
        get() = runCatching { Upstream.valueOf(sp.getString(KEY_UPSTREAM, null)!!) }
            .getOrDefault(Upstream.MOBILE)
        set(value) = sp.edit { putString(KEY_UPSTREAM, value.name) }

    /**
     * Wi-Fi Direct network name. Stable across sessions so the computer can
     * reconnect automatically. Wi-Fi Direct requires the "DIRECT-xy" prefix.
     */
    val wifiNetworkName: String
        get() = "DIRECT-OT-OpenTether-" + stable(KEY_WIFI_SUFFIX, 4, ALNUM)

    val wifiPassphrase: String
        get() = stable(KEY_WIFI_PASS, 12, PASS_CHARS)

    /** Also serve an HTTP/SOCKS5 proxy on the Wi-Fi Direct group for devices without the client. */
    var proxyEnabled: Boolean
        get() = sp.getBoolean(KEY_PROXY, false)
        set(value) = sp.edit { putBoolean(KEY_PROXY, value) }

    fun resetWifiCredentials() = sp.edit { remove(KEY_WIFI_SUFFIX); remove(KEY_WIFI_PASS) }

    private fun stable(key: String, length: Int, alphabet: String): String {
        sp.getString(key, null)?.let { return it }
        val rnd = SecureRandom()
        val value = String(CharArray(length) { alphabet[rnd.nextInt(alphabet.length)] })
        sp.edit { putString(key, value) }
        return value
    }

    private companion object {
        const val KEY_UPSTREAM = "upstream"
        const val KEY_PROXY = "proxy_enabled"
        const val KEY_WIFI_SUFFIX = "wifi_suffix"
        const val KEY_WIFI_PASS = "wifi_pass"
        const val ALNUM = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
        // No look-alike characters, so the password is easy to type.
        const val PASS_CHARS = "abcdefghjkmnpqrstuvwxyz23456789"
    }
}
