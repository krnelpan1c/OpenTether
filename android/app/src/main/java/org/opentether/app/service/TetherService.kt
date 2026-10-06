package org.opentether.app.service

import android.annotation.SuppressLint
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.hardware.usb.UsbAccessory
import android.hardware.usb.UsbManager
import android.net.wifi.WifiManager
import android.os.PowerManager
import android.os.SystemClock
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import androidx.lifecycle.LifecycleService
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import org.opentether.app.core.Core
import org.opentether.app.core.LogLevel
import org.opentether.app.core.ModeState
import org.opentether.app.core.ModeStatus
import org.opentether.app.core.Prefs
import org.opentether.app.core.RelayStats
import org.opentether.app.core.TetherController
import org.opentether.app.core.Upstream
import org.opentether.app.core.WifiGroup

/**
 * Foreground service that keeps the relay listeners, the upstream network
 * request and the Wi-Fi Direct group alive while sharing.
 */
class TetherService : LifecycleService() {
    private lateinit var prefs: Prefs
    private lateinit var upstream: UpstreamNetwork
    private lateinit var wifiHost: WifiDirectHost

    private var wakeLock: PowerManager.WakeLock? = null
    private var wifiLock: WifiManager.WifiLock? = null
    private var statsJob: Job? = null
    private var wifiJob: Job? = null
    private var accessoryJob: Job? = null
    private var accessoryWatchJob: Job? = null
    private val usbManager by lazy { getSystemService(UsbManager::class.java) }
    private var inForeground = false

    override fun onCreate() {
        super.onCreate()
        prefs = Prefs(this)
        upstream = UpstreamNetwork(this)
        wifiHost = WifiDirectHost(this)
        upstream.start(prefs.upstream)
        ContextCompat.registerReceiver(
            this, permissionReceiver, IntentFilter(ACTION_USB_PERMISSION), ContextCompat.RECEIVER_NOT_EXPORTED,
        )
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        super.onStartCommand(intent, flags, startId)
        when (intent?.action) {
            ACTION_START_USB -> {
                startUsb()
                startAccessoryLoop()
                startAccessoryWatch()
            }
            ACTION_ACCESSORY_ATTACHED -> {
                startUsb()
                startAccessoryLoop()
                startAccessoryWatch()
            }
            ACTION_STOP_USB -> stopUsb()
            ACTION_START_WIFI -> startWifi()
            ACTION_STOP_WIFI -> stopWifi()
            ACTION_SET_UPSTREAM -> {
                val mode = runCatching { Upstream.valueOf(intent.getStringExtra(EXTRA_UPSTREAM)!!) }
                    .getOrDefault(Upstream.MOBILE)
                prefs.upstream = mode
                upstream.start(mode)
            }
            ACTION_SET_PROXY -> {
                prefs.proxyEnabled = intent.getBooleanExtra(EXTRA_ENABLED, false)
                if (prefs.proxyEnabled) startProxy() else stopProxy()
            }
            ACTION_STOP_ALL -> {
                stopUsb()
                stopWifi()
            }
        }
        updateForeground()
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        statsJob?.cancel()
        wifiJob?.cancel()
        accessoryJob?.cancel()
        accessoryWatchJob?.cancel()
        unregisterReceiver(permissionReceiver)
        Core.relay.stop()
        wifiHost.stop()
        upstream.stop()
        releaseLocks()
        TetherController.update {
            it.copy(
                usb = it.usb.takeIf { m -> m.status == ModeStatus.Error } ?: ModeState(),
                wifi = it.wifi.takeIf { m -> m.status == ModeStatus.Error } ?: ModeState(),
                wifiGroup = null, proxyRunning = false, usbAccessory = false, stats = RelayStats(), rateUp = 0, rateDown = 0,
            )
        }
        super.onDestroy()
    }

    // --- Modes ------------------------------------------------------------

    private fun startUsb() {
        enterForeground()
        if (TetherController.state.value.usb.status == ModeStatus.On) return
        try {
            Core.relay.startUSB()
            TetherController.update { it.copy(usb = ModeState(ModeStatus.On)) }
            TetherController.log(LogLevel.Info, "USB sharing started; waiting for the desktop client")
        } catch (e: Exception) {
            TetherController.update { it.copy(usb = ModeState(ModeStatus.Error, e.message)) }
            TetherController.log(LogLevel.Error, "USB sharing failed: ${e.message}")
        }
    }

    private fun stopUsb() {
        accessoryWatchJob?.cancel()
        accessoryWatchJob = null
        accessoryJob?.cancel()
        accessoryJob = null
        // Also ends a blocked serveAccessory call.
        Core.relay.stopUSB()
        TetherController.update { it.copy(usb = ModeState(), usbAccessory = false) }
    }

    /** The OpenTether desktop accessory, if attached (permission not implied). */
    private fun attachedAccessory(): UsbAccessory? =
        usbManager.accessoryList?.firstOrNull { it.manufacturer == ACCESSORY_MANUFACTURER }

    private fun findAccessory(): UsbAccessory? = attachedAccessory()?.takeIf { usbManager.hasPermission(it) }

    /**
     * While USB sharing is on, watch for the accessory ourselves instead of
     * relying only on the system's "open with" matching: ask for permission
     * when needed and start serving once granted.
     */
    private fun startAccessoryWatch() {
        if (accessoryWatchJob?.isActive == true) return
        accessoryWatchJob = lifecycleScope.launch {
            var reported: String? = null
            var asked = false
            while (isActive) {
                val all = usbManager.accessoryList.orEmpty()
                val summary = all.joinToString { "${it.manufacturer} / ${it.model}" }
                if (summary != reported) {
                    reported = summary
                    if (all.isNotEmpty()) TetherController.log(LogLevel.Info, "USB accessory attached: $summary")
                }
                val acc = attachedAccessory()
                when {
                    acc == null -> asked = false
                    usbManager.hasPermission(acc) -> startAccessoryLoop()
                    !asked -> {
                        asked = true
                        requestAccessoryPermission(acc)
                    }
                }
                delay(2000)
            }
        }
    }

    private fun requestAccessoryPermission(acc: UsbAccessory) {
        // The system fills in EXTRA_PERMISSION_GRANTED, so the intent must be mutable.
        val intent = Intent(ACTION_USB_PERMISSION).setPackage(packageName)
        val pending = PendingIntent.getBroadcast(this, 2, intent, PendingIntent.FLAG_MUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        usbManager.requestPermission(acc, pending)
    }

    private val permissionReceiver = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            if (intent.getBooleanExtra(UsbManager.EXTRA_PERMISSION_GRANTED, false)) {
                startAccessoryLoop()
            } else {
                TetherController.log(LogLevel.Warn, "USB accessory permission denied")
            }
        }
    }

    /**
     * Serves the desktop over USB accessory mode for as long as the accessory
     * stays attached. The desktop may disconnect and reconnect without
     * unplugging, so the accessory is reopened after every session; opening
     * fails until the previous descriptor is fully closed, hence the retry.
     */
    private fun startAccessoryLoop() {
        if (accessoryJob?.isActive == true || findAccessory() == null) return
        accessoryJob = lifecycleScope.launch(Dispatchers.IO) {
            while (isActive) {
                val accessory = findAccessory() ?: break
                val pfd = usbManager.openAccessory(accessory)
                if (pfd == null) {
                    delay(500)
                    continue
                }
                TetherController.update { it.copy(usbAccessory = true) }
                TetherController.log(LogLevel.Info, "USB accessory link open (no USB debugging needed)")
                try {
                    Core.relay.serveAccessory(pfd.detachFd())
                } catch (e: Exception) {
                    TetherController.log(LogLevel.Info, "USB accessory session ended: ${e.message}")
                }
                TetherController.update { it.copy(usbAccessory = false) }
                delay(300)
            }
        }
    }

    private fun startWifi() {
        if (wifiJob?.isActive == true) return
        enterForeground()
        TetherController.update { it.copy(wifi = ModeState(ModeStatus.Starting)) }
        wifiJob = lifecycleScope.launch {
            try {
                val group = wifiHost.start(prefs.wifiNetworkName, prefs.wifiPassphrase)
                Core.relay.startWifi(group.address, Core.WIFI_PORT)
                val uri = Core.relay.pairingURI(
                    Core.deviceName, group.address, Core.WIFI_PORT, group.ssid, group.passphrase,
                )
                TetherController.update {
                    it.copy(
                        wifi = ModeState(ModeStatus.On),
                        wifiGroup = WifiGroup(group.ssid, group.passphrase, group.address, Core.WIFI_PORT, uri),
                    )
                }
                TetherController.log(LogLevel.Info, "Wi-Fi Direct group ${group.ssid} is up at ${group.address}")
                acquireWifiLock()
                if (prefs.proxyEnabled) startProxy()
            } catch (e: Exception) {
                wifiHost.stop()
                TetherController.update { it.copy(wifi = ModeState(ModeStatus.Error, e.message), wifiGroup = null) }
                TetherController.log(LogLevel.Error, "Wi-Fi Direct failed: ${e.message}")
            }
            updateForeground()
        }
    }

    private fun stopWifi() {
        wifiJob?.cancel()
        wifiJob = null
        Core.relay.stopWifi()
        stopProxy()
        wifiHost.stop()
        wifiLock?.takeIf { it.isHeld }?.release()
        wifiLock = null
        TetherController.update { it.copy(wifi = ModeState(), wifiGroup = null) }
    }

    /** Serves the proxy on the Wi-Fi Direct address, once the group is up. */
    private fun startProxy() {
        val group = TetherController.state.value.wifiGroup ?: return
        try {
            Core.relay.startProxy(group.address, Core.PROXY_PORT)
            TetherController.update { it.copy(proxyRunning = true) }
            TetherController.log(LogLevel.Info, "Proxy for other devices at ${group.address}:${Core.PROXY_PORT}")
        } catch (e: Exception) {
            TetherController.log(LogLevel.Error, "Proxy failed to start: ${e.message}")
        }
    }

    private fun stopProxy() {
        Core.relay.stopProxy()
        TetherController.update { it.copy(proxyRunning = false) }
    }

    // --- Foreground lifecycle --------------------------------------------------

    private fun enterForeground() {
        if (inForeground) return
        ServiceCompat.startForeground(
            this,
            Notifications.ID,
            Notifications.build(this, TetherController.state.value),
            ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE,
        )
        inForeground = true
        acquireWakeLock()
        startStatsLoop()
    }

    /** Leaves the foreground and stops once no mode is active. */
    private fun updateForeground() {
        val state = TetherController.state.value
        if (state.sharing) return
        statsJob?.cancel()
        releaseLocks()
        if (inForeground) {
            ServiceCompat.stopForeground(this, ServiceCompat.STOP_FOREGROUND_REMOVE)
            inForeground = false
        }
        Core.relay.stop()
        stopSelf()
    }

    @SuppressLint("MissingPermission")
    private fun startStatsLoop() {
        statsJob?.cancel()
        statsJob = lifecycleScope.launch {
            var last = RelayStats()
            var lastAt = SystemClock.elapsedRealtime()
            var tick = 0
            while (isActive) {
                delay(1000)
                val stats = RelayStats.parse(Core.relay.statsJSON())
                val now = SystemClock.elapsedRealtime()
                val secs = ((now - lastAt).coerceAtLeast(1)) / 1000.0
                val up = ((stats.bytesUp - last.bytesUp).coerceAtLeast(0) / secs).toLong()
                val down = ((stats.bytesDown - last.bytesDown).coerceAtLeast(0) / secs).toLong()
                if (stats.sessions != last.sessions) {
                    TetherController.log(
                        LogLevel.Info,
                        if (stats.clients.isEmpty()) "No computers connected" else "Connected: ${stats.clients.joinToString()}",
                    )
                }
                last = stats
                lastAt = now
                TetherController.update { it.copy(stats = stats, rateUp = up, rateDown = down) }
                if (tick++ % 2 == 0 && inForeground && NotificationManagerCompat.from(this@TetherService).areNotificationsEnabled()) {
                    NotificationManagerCompat.from(this@TetherService)
                        .notify(Notifications.ID, Notifications.build(this@TetherService, TetherController.state.value))
                }
            }
        }
    }

    // Held for exactly as long as sharing is active and released in updateForeground/onDestroy.
    @SuppressLint("WakelockTimeout")
    private fun acquireWakeLock() {
        if (wakeLock?.isHeld == true) return
        wakeLock = getSystemService(PowerManager::class.java)
            .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "OpenTether:relay")
            .apply { setReferenceCounted(false); acquire() }
    }

    private fun acquireWifiLock() {
        if (wifiLock?.isHeld == true) return
        wifiLock = getSystemService(WifiManager::class.java)
            ?.createWifiLock(WifiManager.WIFI_MODE_FULL_LOW_LATENCY, "OpenTether:wifi-direct")
            ?.apply { setReferenceCounted(false); acquire() }
    }

    private fun releaseLocks() {
        wakeLock?.takeIf { it.isHeld }?.release()
        wakeLock = null
        wifiLock?.takeIf { it.isHeld }?.release()
        wifiLock = null
    }

    companion object {
        const val ACTION_START_USB = "org.opentether.action.START_USB"
        const val ACTION_STOP_USB = "org.opentether.action.STOP_USB"
        const val ACTION_START_WIFI = "org.opentether.action.START_WIFI"
        const val ACTION_STOP_WIFI = "org.opentether.action.STOP_WIFI"
        const val ACTION_SET_UPSTREAM = "org.opentether.action.SET_UPSTREAM"
        const val ACTION_STOP_ALL = "org.opentether.action.STOP_ALL"
        const val ACTION_ACCESSORY_ATTACHED = "org.opentether.action.ACCESSORY_ATTACHED"
        const val ACTION_SET_PROXY = "org.opentether.action.SET_PROXY"
        const val EXTRA_UPSTREAM = "upstream"
        const val EXTRA_ENABLED = "enabled"

        // Must match AccessoryManufacturer in desktop/usb/aoa.go.
        private const val ACCESSORY_MANUFACTURER = "OpenTether"
        private const val ACTION_USB_PERMISSION = "org.opentether.action.USB_PERMISSION"

        /** Starting a mode needs a foreground-service start. */
        fun start(context: Context, action: String) {
            ContextCompat.startForegroundService(context, Intent(context, TetherService::class.java).setAction(action))
        }

        /** Commands that don't start sharing; ignored when the service isn't running. */
        fun send(context: Context, action: String, configure: Intent.() -> Unit = {}) {
            if (!TetherController.state.value.sharing) return
            context.startService(Intent(context, TetherService::class.java).setAction(action).apply(configure))
        }
    }
}
