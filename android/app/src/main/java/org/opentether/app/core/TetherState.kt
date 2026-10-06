package org.opentether.app.core

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import org.json.JSONObject

/** Where the relay's sockets get their internet from. */
enum class Upstream { MOBILE, ANY }

enum class ModeStatus { Off, Starting, On, Error }

data class ModeState(val status: ModeStatus = ModeStatus.Off, val message: String? = null) {
    val isActive: Boolean get() = status == ModeStatus.On || status == ModeStatus.Starting
}

data class WifiGroup(
    val ssid: String,
    val passphrase: String,
    val address: String,
    val port: Int,
    val pairingUri: String,
)

data class UpstreamState(
    val mode: Upstream = Upstream.MOBILE,
    val available: Boolean = false,
    val label: String = "",
    val ipv6: Boolean = false,
)

/** Mirrors relay.Stats in the Go core. */
data class RelayStats(
    val sessions: Int = 0,
    val clients: List<String> = emptyList(),
    val tcpActive: Long = 0,
    val tcpTotal: Long = 0,
    val udpFlows: Long = 0,
    val dnsQueries: Long = 0,
    val bytesUp: Long = 0,
    val bytesDown: Long = 0,
    val errors: Long = 0,
) {
    companion object {
        fun parse(json: String): RelayStats = runCatching {
            val o = JSONObject(json)
            val clients = o.optJSONArray("clients")
            RelayStats(
                sessions = o.optInt("sessions"),
                clients = List(clients?.length() ?: 0) { clients!!.getString(it) },
                tcpActive = o.optLong("tcpActive"),
                tcpTotal = o.optLong("tcpTotal"),
                udpFlows = o.optLong("udpFlows"),
                dnsQueries = o.optLong("dnsQueries"),
                bytesUp = o.optLong("bytesUp"),
                bytesDown = o.optLong("bytesDown"),
                errors = o.optLong("errors"),
            )
        }.getOrDefault(RelayStats())
    }
}

data class TetherState(
    val usb: ModeState = ModeState(),
    val wifi: ModeState = ModeState(),
    val wifiGroup: WifiGroup? = null,
    /** True while the proxy for devices without the client is listening. */
    val proxyRunning: Boolean = false,
    /** True while a computer is connected through USB accessory mode. */
    val usbAccessory: Boolean = false,
    val upstream: UpstreamState = UpstreamState(),
    val stats: RelayStats = RelayStats(),
    /** Bytes per second, computer → internet. */
    val rateUp: Long = 0,
    /** Bytes per second, internet → computer. */
    val rateDown: Long = 0,
    val fingerprint: String = "",
    val pairingCode: String = "",
) {
    val sharing: Boolean get() = usb.isActive || wifi.isActive
    val connected: Boolean get() = stats.sessions > 0
}

enum class LogLevel { Debug, Info, Warn, Error }

data class LogEntry(val time: Long, val level: LogLevel, val message: String)

/** Single source of truth shared by the service, notification and UI. */
object TetherController {
    private val _state = MutableStateFlow(TetherState())
    val state: StateFlow<TetherState> = _state.asStateFlow()

    private val _log = MutableStateFlow<List<LogEntry>>(emptyList())
    val log: StateFlow<List<LogEntry>> = _log.asStateFlow()

    fun update(block: (TetherState) -> TetherState) = _state.update(block)

    fun log(level: LogLevel, message: String) {
        val entry = LogEntry(System.currentTimeMillis(), level, message)
        _log.update { (it + entry).takeLast(MAX_LOG) }
    }

    fun clearLog() = _log.update { emptyList() }

    private const val MAX_LOG = 400
}
