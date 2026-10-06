package org.opentether.app.service

import android.Manifest
import android.annotation.SuppressLint
import android.content.Context
import android.content.pm.PackageManager
import android.net.wifi.WifiManager
import android.net.wifi.p2p.WifiP2pConfig
import android.net.wifi.p2p.WifiP2pGroup
import android.net.wifi.p2p.WifiP2pInfo
import android.net.wifi.p2p.WifiP2pManager
import android.os.Build
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException
import kotlinx.coroutines.delay
import kotlinx.coroutines.suspendCancellableCoroutine

class WifiDirectException(message: String) : Exception(message)

/**
 * Hosts a Wi-Fi Direct group. Computers join it like an ordinary WPA2
 * network; the phone is the group owner (normally at 192.168.49.1) and does
 * not route between the group and mobile data, so the only way out is
 * through the relay.
 */
class WifiDirectHost(private val context: Context) {
    data class Group(val ssid: String, val passphrase: String, val address: String)

    private val manager: WifiP2pManager? = context.getSystemService(WifiP2pManager::class.java)
    private var channel: WifiP2pManager.Channel? = null

    fun hasPermission(): Boolean {
        val perm = if (Build.VERSION.SDK_INT >= 33) {
            Manifest.permission.NEARBY_WIFI_DEVICES
        } else {
            Manifest.permission.ACCESS_FINE_LOCATION
        }
        return context.checkSelfPermission(perm) == PackageManager.PERMISSION_GRANTED
    }

    @SuppressLint("MissingPermission")
    suspend fun start(networkName: String, passphrase: String): Group {
        val mgr = manager ?: throw WifiDirectException("This phone does not support Wi-Fi Direct.")
        if (!hasPermission()) throw WifiDirectException("Allow the Nearby devices permission to use Wi-Fi Direct.")
        val wifi = context.getSystemService(WifiManager::class.java)
        if (wifi?.isWifiEnabled == false) {
            throw WifiDirectException("Turn on Wi-Fi. The phone doesn't need to join a network.")
        }
        val ch = channel ?: mgr.initialize(context, context.mainLooper, null).also { channel = it }

        // A group left over from a previous run would block createGroup.
        runCatching { action { mgr.removeGroup(ch, it) } }

        try {
            createGroup(mgr, ch, networkName, passphrase, WifiP2pConfig.GROUP_OWNER_BAND_5GHZ)
        } catch (_: WifiDirectException) {
            // 5 GHz is unavailable when the radio is busy on a 2.4 GHz channel.
            createGroup(mgr, ch, networkName, passphrase, WifiP2pConfig.GROUP_OWNER_BAND_AUTO)
        }

        repeat(40) {
            val group = groupInfo(mgr, ch)
            val info = connectionInfo(mgr, ch)
            val owner = info?.groupOwnerAddress?.hostAddress
            if (group != null && info?.groupFormed == true && owner != null) {
                return Group(group.networkName, group.passphrase ?: passphrase, owner)
            }
            delay(250)
        }
        throw WifiDirectException("The Wi-Fi Direct group did not start. Toggle Wi-Fi and try again.")
    }

    fun stop() {
        val mgr = manager ?: return
        val ch = channel ?: return
        mgr.removeGroup(ch, null)
    }

    @SuppressLint("MissingPermission")
    private suspend fun createGroup(
        mgr: WifiP2pManager,
        ch: WifiP2pManager.Channel,
        name: String,
        passphrase: String,
        band: Int,
    ) {
        val config = WifiP2pConfig.Builder()
            .setNetworkName(name)
            .setPassphrase(passphrase)
            .setGroupOperatingBand(band)
            .enablePersistentMode(false)
            .build()
        action { mgr.createGroup(ch, config, it) }
    }

    @SuppressLint("MissingPermission")
    private suspend fun groupInfo(mgr: WifiP2pManager, ch: WifiP2pManager.Channel): WifiP2pGroup? =
        suspendCancellableCoroutine { cont -> mgr.requestGroupInfo(ch) { cont.resume(it) } }

    private suspend fun connectionInfo(mgr: WifiP2pManager, ch: WifiP2pManager.Channel): WifiP2pInfo? =
        suspendCancellableCoroutine { cont -> mgr.requestConnectionInfo(ch) { cont.resume(it) } }

    private suspend fun action(block: (WifiP2pManager.ActionListener) -> Unit) =
        suspendCancellableCoroutine { cont ->
            block(object : WifiP2pManager.ActionListener {
                override fun onSuccess() = cont.resume(Unit)
                override fun onFailure(reason: Int) = cont.resumeWithException(WifiDirectException(reasonText(reason)))
            })
        }

    private fun reasonText(reason: Int) = when (reason) {
        WifiP2pManager.P2P_UNSUPPORTED -> "This phone does not support Wi-Fi Direct."
        WifiP2pManager.BUSY -> "Wi-Fi Direct is busy. Turn Wi-Fi off and on, then try again."
        else -> "Wi-Fi Direct failed. Make sure Wi-Fi is on and the system hotspot is off."
    }
}
