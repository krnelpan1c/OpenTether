package org.opentether.app.service

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.telephony.TelephonyManager
import java.net.Inet6Address
import org.opentether.app.core.Core
import org.opentether.app.core.TetherController
import org.opentether.app.core.Upstream
import org.opentether.app.core.UpstreamState

/**
 * Tracks the network the relay uses. In [Upstream.MOBILE] mode it requests
 * the cellular network explicitly, which keeps mobile data up even while the
 * phone is on Wi-Fi (the same way the system hotspot does).
 */
class UpstreamNetwork(context: Context) {
    private val cm = context.getSystemService(ConnectivityManager::class.java)
    private val telephony = context.getSystemService(TelephonyManager::class.java)

    private var callback: ConnectivityManager.NetworkCallback? = null
    private var mode = Upstream.MOBILE

    @Volatile private var network: Network? = null
    @Volatile private var props: LinkProperties? = null
    @Volatile private var caps: NetworkCapabilities? = null

    fun start(mode: Upstream) {
        stop()
        this.mode = mode
        Core.platform.bindRequired = mode == Upstream.MOBILE
        val cb = object : ConnectivityManager.NetworkCallback() {
            override fun onAvailable(n: Network) {
                network = n
                publish()
            }

            override fun onCapabilitiesChanged(n: Network, c: NetworkCapabilities) {
                if (n == network) {
                    caps = c
                    publish()
                }
            }

            override fun onLinkPropertiesChanged(n: Network, lp: LinkProperties) {
                if (n == network) {
                    props = lp
                    publish()
                }
            }

            override fun onLost(n: Network) {
                if (n == network) {
                    network = null
                    props = null
                    caps = null
                    publish()
                }
            }
        }
        when (mode) {
            Upstream.MOBILE -> cm.requestNetwork(
                NetworkRequest.Builder()
                    .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
                    .addTransportType(NetworkCapabilities.TRANSPORT_CELLULAR)
                    .build(),
                cb,
            )
            Upstream.ANY -> cm.registerDefaultNetworkCallback(cb)
        }
        callback = cb
        publish()
    }

    fun stop() {
        callback?.let { runCatching { cm.unregisterNetworkCallback(it) } }
        callback = null
        network = null
        props = null
        caps = null
        Core.platform.network = null
    }

    private fun publish() {
        val n = network
        val ipv6 = props?.let(::hasGlobalIPv6) ?: false
        Core.platform.network = n
        Core.platform.ipv6 = ipv6
        TetherController.update {
            it.copy(upstream = UpstreamState(mode = mode, available = n != null, label = label(), ipv6 = ipv6))
        }
    }

    private fun label(): String {
        val c = caps
        val carrier = telephony?.networkOperatorName?.takeIf { it.isNotBlank() }
        return when {
            network == null -> ""
            c == null -> carrier ?: ""
            c.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> carrier ?: "Mobile data"
            c.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "Wi-Fi"
            c.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "Ethernet"
            c.hasTransport(NetworkCapabilities.TRANSPORT_VPN) -> "VPN"
            else -> "Network"
        }
    }

    private fun hasGlobalIPv6(lp: LinkProperties): Boolean =
        lp.linkAddresses.any { la ->
            val a = la.address
            a is Inet6Address && !a.isLinkLocalAddress && !a.isSiteLocalAddress && !a.isLoopbackAddress
        } && lp.routes.any { it.isDefaultRoute && it.destination.address is Inet6Address }
}
