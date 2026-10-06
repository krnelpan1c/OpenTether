package org.opentether.app.core

import android.content.Context
import android.os.Build
import android.provider.Settings
import android.util.Log
import org.opentether.app.service.AndroidPlatform
import org.opentether.core.mobile.Mobile
import org.opentether.core.mobile.Service

/**
 * Process-wide handle on the Go relay core. The identity and pairing token
 * live in the app's files directory, so they survive restarts.
 */
object Core {
    val platform = AndroidPlatform()

    lateinit var relay: Service
        private set

    lateinit var deviceName: String
        private set

    val version: String get() = Mobile.version()

    fun init(context: Context) {
        deviceName = Settings.Global.getString(context.contentResolver, Settings.Global.DEVICE_NAME)
            ?.takeIf { it.isNotBlank() }
            ?: "${Build.MANUFACTURER.replaceFirstChar { it.uppercase() }} ${Build.MODEL}"
        relay = Mobile.newService(context.filesDir.absolutePath, deviceName, platform)
        TetherController.update {
            it.copy(fingerprint = relay.shortFingerprint(), pairingCode = relay.token())
        }
        Log.i(TAG, "core ${Mobile.version()} ready, fingerprint ${relay.shortFingerprint()}")
    }

    /** Issues a new pairing token, disconnecting every computer. */
    fun rotatePairing(): String {
        val token = relay.rotateToken()
        TetherController.update { it.copy(pairingCode = token) }
        return token
    }

    const val TAG = "OpenTether"
    const val WIFI_PORT = 47100
    const val PROXY_PORT = 8080
}
