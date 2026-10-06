package org.opentether.app.ui

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.togetherWith
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Info
import androidx.compose.material.icons.automirrored.outlined.ReceiptLong
import androidx.compose.material.icons.outlined.WifiTethering
import androidx.compose.material.icons.rounded.DeleteSweep
import androidx.compose.material.icons.rounded.Info
import androidx.compose.material.icons.automirrored.rounded.ReceiptLong
import androidx.compose.material.icons.rounded.WifiTethering
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LargeFlexibleTopAppBar
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.ShortNavigationBar
import androidx.compose.material3.ShortNavigationBarItem
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.input.nestedscroll.nestedScroll
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import org.opentether.app.R
import org.opentether.app.core.TetherController
import org.opentether.app.core.TetherState
import org.opentether.app.ui.about.AboutScreen
import org.opentether.app.ui.activity.ActivityScreen
import org.opentether.app.ui.share.ShareScreen

private enum class Destination(val label: Int, val selectedIcon: ImageVector, val icon: ImageVector) {
    Share(R.string.nav_share, Icons.Rounded.WifiTethering, Icons.Outlined.WifiTethering),
    Activity(R.string.nav_activity, Icons.AutoMirrored.Rounded.ReceiptLong, Icons.AutoMirrored.Outlined.ReceiptLong),
    About(R.string.nav_about, Icons.Rounded.Info, Icons.Outlined.Info),
}

@Composable
fun OpenTetherRoot() {
    val state by TetherController.state.collectAsStateWithLifecycle()
    val log by TetherController.log.collectAsStateWithLifecycle()
    var destination by rememberSaveable { mutableStateOf(Destination.Share) }
    val scrollBehavior = TopAppBarDefaults.exitUntilCollapsedScrollBehavior()

    Scaffold(
        modifier = Modifier.nestedScroll(scrollBehavior.nestedScrollConnection),
        topBar = {
            LargeFlexibleTopAppBar(
                title = {
                    Text(
                        if (destination == Destination.Share) stringResource(R.string.app_name)
                        else stringResource(destination.label),
                    )
                },
                subtitle = { Text(subtitle(destination, state, log.size)) },
                actions = {
                    if (destination == Destination.Activity && log.isNotEmpty()) {
                        IconButton(onClick = TetherController::clearLog) {
                            Icon(Icons.Rounded.DeleteSweep, contentDescription = stringResource(R.string.activity_clear))
                        }
                    }
                },
                scrollBehavior = scrollBehavior,
            )
        },
        bottomBar = {
            ShortNavigationBar {
                Destination.entries.forEach { d ->
                    val selected = d == destination
                    ShortNavigationBarItem(
                        selected = selected,
                        onClick = { destination = d },
                        icon = { Icon(if (selected) d.selectedIcon else d.icon, contentDescription = null) },
                        label = { Text(stringResource(d.label)) },
                    )
                }
            }
        },
    ) { padding ->
        val motion = MaterialTheme.motionScheme
        AnimatedContent(
            targetState = destination,
            transitionSpec = {
                fadeIn(motion.defaultEffectsSpec()) togetherWith
                    fadeOut(motion.fastEffectsSpec())
            },
            label = "destination",
        ) { d ->
            when (d) {
                Destination.Share -> ShareScreen(state, padding)
                Destination.Activity -> ActivityScreen(log, padding)
                Destination.About -> AboutScreen(state, padding)
            }
        }
    }
}

@Composable
private fun subtitle(destination: Destination, state: TetherState, events: Int): String = when (destination) {
    Destination.Share -> when {
        state.connected -> stringResource(R.string.status_sharing)
        state.sharing -> stringResource(R.string.status_waiting)
        else -> stringResource(R.string.status_idle)
    }
    Destination.Activity -> pluralStringResource(R.plurals.activity_events, events, events)
    Destination.About -> state.fingerprint
}
