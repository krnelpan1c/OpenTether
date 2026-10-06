package org.opentether.app.ui.share

import android.content.ActivityNotFoundException
import android.content.Context
import android.content.Intent
import android.provider.Settings
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.Computer
import androidx.compose.material.icons.rounded.DataUsage
import androidx.compose.material.icons.rounded.Dns
import androidx.compose.material.icons.rounded.ErrorOutline
import androidx.compose.material.icons.rounded.Share
import androidx.compose.material.icons.rounded.SwapVert
import androidx.compose.material.icons.rounded.Usb
import androidx.compose.material.icons.rounded.Waves
import androidx.compose.material.icons.rounded.WifiTethering
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.ContainedLoadingIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import org.opentether.app.R
import org.opentether.app.core.Core
import org.opentether.app.core.ModeState
import org.opentether.app.core.ModeStatus
import org.opentether.app.core.RelayStats
import org.opentether.app.core.TetherState
import org.opentether.app.core.Upstream
import org.opentether.app.core.WifiGroup
import org.opentether.app.ui.components.Badge
import org.opentether.app.ui.components.CommandChip
import org.opentether.app.ui.components.ConnectedToggleGroup
import org.opentether.app.ui.components.CopyRow
import org.opentether.app.ui.components.ExpressiveCard
import org.opentether.app.ui.components.IconLabel
import org.opentether.app.ui.components.QrCode
import org.opentether.app.ui.components.ToggleOption
import org.opentether.app.ui.components.copyToClipboard
import org.opentether.app.util.formatBytes

@Composable
fun UsbCard(mode: ModeState, accessory: Boolean, onRetry: () -> Unit) {
    val context = LocalContext.current
    ExpressiveCard {
        IconLabel(Icons.Rounded.Usb, stringResource(R.string.usb_title))
        if (mode.status == ModeStatus.Error) {
            ErrorBlock(mode.message, onRetry)
            return@ExpressiveCard
        }
        if (accessory) {
            Surface(shape = MaterialTheme.shapes.medium, color = MaterialTheme.colorScheme.secondaryContainer) {
                Text(
                    stringResource(R.string.usb_accessory_connected),
                    color = MaterialTheme.colorScheme.onSecondaryContainer,
                    style = MaterialTheme.typography.bodyMedium,
                    modifier = Modifier.fillMaxWidth().padding(16.dp),
                )
            }
            return@ExpressiveCard
        }
        Step(1, stringResource(R.string.usb_step_1))
        Step(2, stringResource(R.string.usb_step_2))
        CommandChip("opentether usb")
        Step(3, stringResource(R.string.usb_step_3))
        if (!isAdbEnabled(context)) {
            Surface(shape = MaterialTheme.shapes.medium, color = MaterialTheme.colorScheme.tertiaryContainer) {
                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(
                        stringResource(R.string.usb_debugging_off),
                        color = MaterialTheme.colorScheme.onTertiaryContainer,
                        style = MaterialTheme.typography.bodyMedium,
                    )
                    OutlinedButton(onClick = { openDeveloperOptions(context) }, shapes = ButtonDefaults.shapes()) {
                        Text(stringResource(R.string.usb_open_dev_options))
                    }
                }
            }
        }
    }
}

@Composable
fun WifiCard(
    mode: ModeState,
    group: WifiGroup?,
    proxyEnabled: Boolean,
    proxyRunning: Boolean,
    onProxyChange: (Boolean) -> Unit,
    onRetry: () -> Unit,
) {
    val context = LocalContext.current
    ExpressiveCard {
        IconLabel(Icons.Rounded.WifiTethering, stringResource(R.string.wifi_title))
        when {
            mode.status == ModeStatus.Error -> ErrorBlock(mode.message, onRetry)
            group == null -> Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                ContainedLoadingIndicator(Modifier.size(48.dp))
                Text(stringResource(R.string.wifi_starting), style = MaterialTheme.typography.bodyLarge)
            }
            else -> {
                Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.Center) {
                    QrCode(group.pairingUri, size = 200.dp)
                }
                CopyRow(stringResource(R.string.wifi_network), group.ssid)
                CopyRow(stringResource(R.string.wifi_password), group.passphrase, monospace = true)
                Text(
                    stringResource(R.string.wifi_instructions),
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                val command = "opentether wifi --pair \"${group.pairingUri}\""
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    Button(
                        onClick = { copyToClipboard(context, "Pairing command", command) },
                        shapes = ButtonDefaults.shapes(),
                        modifier = Modifier.weight(1f),
                    ) {
                        Text(stringResource(R.string.wifi_copy_command), textAlign = TextAlign.Center)
                    }
                    OutlinedButton(
                        onClick = { shareText(context, command) },
                        shapes = ButtonDefaults.shapes(),
                    ) {
                        Icon(Icons.Rounded.Share, contentDescription = null, modifier = Modifier.size(ButtonDefaults.IconSize))
                        Text(stringResource(R.string.wifi_share_link), modifier = Modifier.padding(start = 8.dp))
                    }
                }
                ProxySection(group, proxyEnabled, proxyRunning, onProxyChange)
            }
        }
    }
}

/** Proxy-only mode: lets devices without the desktop client use the group. */
@Composable
private fun ProxySection(group: WifiGroup, enabled: Boolean, running: Boolean, onChange: (Boolean) -> Unit) {
    HorizontalDivider()
    Row(verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(stringResource(R.string.proxy_title), style = MaterialTheme.typography.titleSmall)
            Text(
                stringResource(R.string.proxy_subtitle),
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        Switch(checked = enabled, onCheckedChange = onChange, modifier = Modifier.padding(start = 12.dp))
    }
    if (enabled && running) {
        val proxy = "${group.address}:${Core.PROXY_PORT}"
        CopyRow(stringResource(R.string.proxy_address), proxy, monospace = true)
        CopyRow(stringResource(R.string.proxy_pac), "http://$proxy/proxy.pac", monospace = true)
        Text(
            stringResource(R.string.proxy_instructions),
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}

@Composable
fun UpstreamCard(
    state: TetherState,
    options: List<ToggleOption>,
    onSelect: (Upstream) -> Unit,
) {
    ExpressiveCard {
        ConnectedToggleGroup(
            options = options,
            onToggle = { index, _ -> onSelect(if (index == 0) Upstream.MOBILE else Upstream.ANY) },
        )
        if (state.sharing) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                val up = state.upstream
                Text(
                    text = if (up.available) up.label.ifEmpty { stringResource(R.string.upstream_mobile) }
                    else stringResource(R.string.upstream_unavailable),
                    style = MaterialTheme.typography.bodyLarge,
                    color = if (up.available) MaterialTheme.colorScheme.onSurface else MaterialTheme.colorScheme.error,
                    modifier = Modifier.weight(1f),
                )
                if (up.available && up.ipv6) Badge(stringResource(R.string.ipv6_badge))
            }
        }
    }
}

@Composable
fun StatsCard(stats: RelayStats) {
    ExpressiveCard {
        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            StatTile(Icons.Rounded.Computer, stats.sessions.toString(), stringResource(R.string.stat_computers), Modifier.weight(1f))
            StatTile(Icons.Rounded.SwapVert, stats.tcpActive.toString(), stringResource(R.string.stat_tcp), Modifier.weight(1f))
        }
        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            StatTile(Icons.Rounded.Waves, stats.udpFlows.toString(), stringResource(R.string.stat_udp), Modifier.weight(1f))
            StatTile(Icons.Rounded.Dns, stats.dnsQueries.toString(), stringResource(R.string.stat_dns), Modifier.weight(1f))
        }
        StatTile(
            Icons.Rounded.DataUsage,
            formatBytes(stats.bytesUp + stats.bytesDown),
            stringResource(R.string.stat_total),
            Modifier.fillMaxWidth(),
        )
    }
}

@Composable
private fun StatTile(icon: ImageVector, value: String, label: String, modifier: Modifier = Modifier) {
    Surface(modifier = modifier, shape = MaterialTheme.shapes.medium, color = MaterialTheme.colorScheme.surfaceContainerHigh) {
        Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(icon, contentDescription = null, tint = MaterialTheme.colorScheme.primary)
            Column(Modifier.padding(start = 12.dp)) {
                Text(value, style = MaterialTheme.typography.titleLarge)
                Text(label, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.onSurfaceVariant)
            }
        }
    }
}

@Composable
private fun Step(number: Int, text: String) {
    Row(verticalAlignment = Alignment.Top) {
        Surface(shape = CircleShape, color = MaterialTheme.colorScheme.secondaryContainer, modifier = Modifier.size(28.dp)) {
            Box(contentAlignment = Alignment.Center) {
                Text(number.toString(), style = MaterialTheme.typography.labelLarge, color = MaterialTheme.colorScheme.onSecondaryContainer)
            }
        }
        Box(Modifier.width(12.dp))
        Text(text, style = MaterialTheme.typography.bodyLarge, modifier = Modifier.padding(top = 2.dp))
    }
}

@Composable
private fun ErrorBlock(message: String?, onRetry: () -> Unit) {
    Surface(shape = MaterialTheme.shapes.medium, color = MaterialTheme.colorScheme.errorContainer) {
        Column(Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Icon(Icons.Rounded.ErrorOutline, contentDescription = null, tint = MaterialTheme.colorScheme.onErrorContainer)
                Text(
                    message ?: "Something went wrong.",
                    color = MaterialTheme.colorScheme.onErrorContainer,
                    style = MaterialTheme.typography.bodyMedium,
                )
            }
            OutlinedButton(onClick = onRetry, shapes = ButtonDefaults.shapes()) { Text("Try again") }
        }
    }
}

private fun isAdbEnabled(context: Context): Boolean =
    Settings.Global.getInt(context.contentResolver, Settings.Global.ADB_ENABLED, 0) == 1

private fun openDeveloperOptions(context: Context) {
    val intent = Intent(Settings.ACTION_APPLICATION_DEVELOPMENT_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    try {
        context.startActivity(intent)
    } catch (_: ActivityNotFoundException) {
        context.startActivity(Intent(Settings.ACTION_DEVICE_INFO_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
    }
}

private fun shareText(context: Context, text: String) {
    val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, text)
    context.startActivity(Intent.createChooser(send, null).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
}
