package org.opentether.app.ui.about

import android.content.Context
import android.content.Intent
import android.provider.Settings
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.BatteryChargingFull
import androidx.compose.material.icons.rounded.Computer
import androidx.compose.material.icons.rounded.Lock
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.MaterialShapes
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.toShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import org.opentether.app.R
import org.opentether.app.core.Core
import org.opentether.app.core.TetherState
import org.opentether.app.ui.components.CopyRow
import org.opentether.app.ui.components.ExpressiveCard
import org.opentether.app.ui.components.IconLabel

@Composable
fun AboutScreen(state: TetherState, contentPadding: PaddingValues) {
    val context = LocalContext.current
    var confirmReset by rememberSaveable { mutableStateOf(false) }

    Column(
        Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(top = contentPadding.calculateTopPadding(), bottom = contentPadding.calculateBottomPadding())
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.CenterHorizontally) {
            Box(
                Modifier.size(112.dp).clip(MaterialShapes.Cookie12Sided.toShape()).background(MaterialTheme.colorScheme.primary),
                contentAlignment = Alignment.Center,
            ) {
                Image(painterResource(R.drawable.ic_launcher_foreground), contentDescription = null, modifier = Modifier.size(112.dp))
            }
            Text(
                stringResource(R.string.about_tagline),
                style = MaterialTheme.typography.bodyLarge,
                textAlign = TextAlign.Center,
                modifier = Modifier.padding(top = 16.dp),
            )
        }

        ExpressiveCard {
            IconLabel(Icons.Rounded.Lock, stringResource(R.string.about_security))
            CopyRow(stringResource(R.string.about_fingerprint), state.fingerprint, monospace = true)
            CopyRow(stringResource(R.string.about_pairing_code), state.pairingCode, monospace = true)
            Text(
                stringResource(R.string.about_reset_pairing_detail),
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            OutlinedButton(onClick = { confirmReset = true }, shapes = ButtonDefaults.shapes()) {
                Text(stringResource(R.string.about_reset_pairing))
            }
        }

        ExpressiveCard {
            IconLabel(Icons.Rounded.BatteryChargingFull, stringResource(R.string.about_background))
            Text(
                stringResource(R.string.about_background_detail),
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            FilledTonalButton(onClick = { openBatterySettings(context) }, shapes = ButtonDefaults.shapes()) {
                Text(stringResource(R.string.about_open_settings))
            }
        }

        ExpressiveCard {
            IconLabel(Icons.Rounded.Computer, stringResource(R.string.about_desktop))
            Text(stringResource(R.string.about_desktop_detail), style = MaterialTheme.typography.bodyMedium)
        }

        Text(
            stringResource(R.string.about_carrier_note),
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(horizontal = 8.dp),
        )
        Text(
            stringResource(R.string.about_version, appVersion(context), Core.version),
            style = MaterialTheme.typography.labelMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(horizontal = 8.dp),
        )
    }

    if (confirmReset) {
        AlertDialog(
            onDismissRequest = { confirmReset = false },
            title = { Text(stringResource(R.string.about_reset_pairing)) },
            text = { Text(stringResource(R.string.about_reset_pairing_detail)) },
            confirmButton = {
                TextButton(onClick = {
                    confirmReset = false
                    Core.rotatePairing()
                }) { Text(stringResource(R.string.about_reset_pairing)) }
            },
            dismissButton = {
                TextButton(onClick = { confirmReset = false }) { Text(stringResource(android.R.string.cancel)) }
            },
        )
    }
}

private fun appVersion(context: Context): String =
    context.packageManager.getPackageInfo(context.packageName, 0).versionName ?: "?"

private fun openBatterySettings(context: Context) {
    context.startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
}
