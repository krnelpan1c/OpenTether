package org.opentether.app.ui.share

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.CellTower
import androidx.compose.material.icons.rounded.Public
import androidx.compose.material.icons.rounded.Usb
import androidx.compose.material.icons.rounded.WifiTethering
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.unit.dp
import org.opentether.app.R
import org.opentether.app.core.ModeState
import org.opentether.app.core.ModeStatus
import org.opentether.app.core.Prefs
import org.opentether.app.core.TetherController
import org.opentether.app.core.TetherState
import org.opentether.app.core.Upstream
import org.opentether.app.service.TetherService
import org.opentether.app.ui.components.ConnectedToggleGroup
import org.opentether.app.ui.components.SectionHeader
import org.opentether.app.ui.components.ToggleOption

@Composable
fun ShareScreen(state: TetherState, contentPadding: PaddingValues) {
    val context = LocalContext.current
    val prefs = remember { Prefs(context) }
    var upstreamMode by rememberSaveable { mutableStateOf(prefs.upstream) }
    var proxyEnabled by rememberSaveable { mutableStateOf(prefs.proxyEnabled) }
    var pendingAction by rememberSaveable { mutableStateOf<String?>(null) }
    val permissionNeeded = stringResource(R.string.wifi_permission_needed)

    val permissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestMultiplePermissions(),
    ) { granted ->
        val action = pendingAction ?: return@rememberLauncherForActivityResult
        pendingAction = null
        // Notifications are optional; Wi-Fi Direct cannot start without its permission.
        val wifiDenied = action == TetherService.ACTION_START_WIFI &&
            wifiPermissions().any { granted[it] == false }
        if (wifiDenied) {
            TetherController.update { it.copy(wifi = ModeState(ModeStatus.Error, permissionNeeded)) }
        } else {
            TetherService.start(context, action)
        }
    }

    fun startMode(action: String) {
        val missing = requiredPermissions(action).filter {
            context.checkSelfPermission(it) != PackageManager.PERMISSION_GRANTED
        }
        if (missing.isEmpty()) {
            TetherService.start(context, action)
        } else {
            pendingAction = action
            permissionLauncher.launch(missing.toTypedArray())
        }
    }

    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(
            start = 16.dp,
            end = 16.dp,
            top = contentPadding.calculateTopPadding() + 8.dp,
            bottom = contentPadding.calculateBottomPadding() + 24.dp,
        ),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        item("hero") {
            StatusHero(state, onStop = { TetherService.send(context, TetherService.ACTION_STOP_ALL) })
        }

        item("modes-header") { SectionHeader(stringResource(R.string.section_share_over)) }
        item("modes") {
            ConnectedToggleGroup(
                options = listOf(
                    ToggleOption(stringResource(R.string.mode_usb), Icons.Rounded.Usb, state.usb.isActive),
                    ToggleOption(stringResource(R.string.mode_wifi), Icons.Rounded.WifiTethering, state.wifi.isActive),
                ),
                onToggle = { index, checked ->
                    when (index) {
                        0 -> if (checked) startMode(TetherService.ACTION_START_USB)
                        else TetherService.send(context, TetherService.ACTION_STOP_USB)
                        1 -> if (checked) startMode(TetherService.ACTION_START_WIFI)
                        else TetherService.send(context, TetherService.ACTION_STOP_WIFI)
                    }
                },
            )
        }

        item("usb") {
            AnimatedVisibility(
                visible = state.usb.status != ModeStatus.Off,
                enter = fadeIn() + expandVertically(),
                exit = fadeOut() + shrinkVertically(),
            ) {
                UsbCard(state.usb, state.usbAccessory, onRetry = { startMode(TetherService.ACTION_START_USB) })
            }
        }
        item("wifi") {
            AnimatedVisibility(
                visible = state.wifi.status != ModeStatus.Off,
                enter = fadeIn() + expandVertically(),
                exit = fadeOut() + shrinkVertically(),
            ) {
                WifiCard(
                    mode = state.wifi,
                    group = state.wifiGroup,
                    proxyEnabled = proxyEnabled,
                    proxyRunning = state.proxyRunning,
                    onProxyChange = { enabled ->
                        proxyEnabled = enabled
                        prefs.proxyEnabled = enabled
                        TetherService.send(context, TetherService.ACTION_SET_PROXY) {
                            putExtra(TetherService.EXTRA_ENABLED, enabled)
                        }
                    },
                    onRetry = { startMode(TetherService.ACTION_START_WIFI) },
                )
            }
        }

        item("upstream-header") { SectionHeader(stringResource(R.string.section_internet_source)) }
        item("upstream") {
            UpstreamCard(
                state = state,
                options = listOf(
                    ToggleOption(stringResource(R.string.upstream_mobile), Icons.Rounded.CellTower, upstreamMode == Upstream.MOBILE),
                    ToggleOption(stringResource(R.string.upstream_any), Icons.Rounded.Public, upstreamMode == Upstream.ANY),
                ),
                onSelect = { mode ->
                    upstreamMode = mode
                    prefs.upstream = mode
                    TetherService.send(context, TetherService.ACTION_SET_UPSTREAM) {
                        putExtra(TetherService.EXTRA_UPSTREAM, mode.name)
                    }
                },
            )
        }

        if (state.sharing) {
            item("stats-header") { SectionHeader(stringResource(R.string.section_statistics)) }
            item("stats") { StatsCard(state.stats) }
        }
    }
}

private fun requiredPermissions(action: String): List<String> = buildList {
    if (Build.VERSION.SDK_INT >= 33) add(Manifest.permission.POST_NOTIFICATIONS)
    if (action == TetherService.ACTION_START_WIFI) addAll(wifiPermissions())
}

/** Android 13+ has a dedicated permission; older versions gate Wi-Fi Direct behind location. */
private fun wifiPermissions(): List<String> =
    if (Build.VERSION.SDK_INT >= 33) {
        listOf(Manifest.permission.NEARBY_WIFI_DEVICES)
    } else {
        listOf(Manifest.permission.ACCESS_FINE_LOCATION, Manifest.permission.ACCESS_COARSE_LOCATION)
    }

