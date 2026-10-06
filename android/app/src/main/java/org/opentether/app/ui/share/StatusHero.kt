package org.opentether.app.ui.share

import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.animation.togetherWith
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.ArrowDownward
import androidx.compose.material.icons.rounded.ArrowUpward
import androidx.compose.material.icons.rounded.Link
import androidx.compose.material.icons.rounded.Usb
import androidx.compose.material.icons.rounded.WifiTethering
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.ContainedLoadingIndicator
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.Icon
import androidx.compose.material3.LinearWavyProgressIndicator
import androidx.compose.material3.MaterialShapes
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.toPath
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Matrix
import androidx.compose.ui.graphics.Outline
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.unit.dp
import androidx.graphics.shapes.Morph
import org.opentether.app.R
import org.opentether.app.core.ModeStatus
import org.opentether.app.core.TetherState
import org.opentether.app.ui.components.ExpressiveCard
import org.opentether.app.util.splitRate

private enum class HeroPhase { Idle, Starting, Waiting, Connected }

private fun TetherState.phase(): HeroPhase = when {
    connected -> HeroPhase.Connected
    usb.status == ModeStatus.On || wifi.status == ModeStatus.On -> HeroPhase.Waiting
    sharing -> HeroPhase.Starting
    else -> HeroPhase.Idle
}

/**
 * The top card. Its badge morphs from a circle (idle) to a scalloped cookie
 * that slowly turns while sharing, then turns faster once a computer is
 * connected.
 */
@Composable
fun StatusHero(state: TetherState, onStop: () -> Unit, modifier: Modifier = Modifier) {
    val phase = state.phase()
    val motion = MaterialTheme.motionScheme
    val colors = MaterialTheme.colorScheme
    ExpressiveCard(
        modifier = modifier,
        color = if (phase == HeroPhase.Connected) colors.primaryContainer else colors.surfaceContainer,
    ) {
        Column(
            Modifier.fillMaxWidth().padding(vertical = 8.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            MorphingBadge(phase, state)

            AnimatedContent(
                targetState = phase,
                transitionSpec = {
                    fadeIn(motion.defaultEffectsSpec()) togetherWith
                        fadeOut(motion.fastEffectsSpec())
                },
                label = "status-text",
            ) { p ->
                Column(horizontalAlignment = Alignment.CenterHorizontally) {
                    Text(
                        text = when (p) {
                            HeroPhase.Idle -> stringResource(R.string.status_idle)
                            HeroPhase.Starting -> stringResource(R.string.status_starting)
                            HeroPhase.Waiting -> stringResource(R.string.status_waiting)
                            HeroPhase.Connected -> stringResource(R.string.status_sharing)
                        },
                        style = MaterialTheme.typography.headlineMedium,
                        textAlign = TextAlign.Center,
                    )
                    val detail = when (p) {
                        HeroPhase.Idle -> stringResource(R.string.status_idle_detail)
                        HeroPhase.Connected -> state.stats.clients.joinToString()
                        else -> if (!state.upstream.available) stringResource(R.string.status_no_upstream) else null
                    }
                    if (!detail.isNullOrEmpty()) {
                        Text(
                            detail,
                            style = MaterialTheme.typography.bodyLarge,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            textAlign = TextAlign.Center,
                        )
                    }
                }
            }

            AnimatedVisibility(
                visible = phase == HeroPhase.Waiting,
                enter = fadeIn() + expandVertically(),
                exit = fadeOut() + shrinkVertically(),
            ) {
                LinearWavyProgressIndicator(Modifier.fillMaxWidth(0.6f))
            }

            AnimatedVisibility(
                visible = phase == HeroPhase.Connected,
                enter = fadeIn() + expandVertically(),
                exit = fadeOut() + shrinkVertically(),
            ) {
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceEvenly) {
                    RateDisplay(Icons.Rounded.ArrowUpward, stringResource(R.string.rate_up), state.rateUp)
                    RateDisplay(Icons.Rounded.ArrowDownward, stringResource(R.string.rate_down), state.rateDown)
                }
            }

            AnimatedVisibility(visible = phase != HeroPhase.Idle) {
                FilledTonalButton(
                    onClick = onStop,
                    shapes = ButtonDefaults.shapes(),
                    modifier = Modifier.height(ButtonDefaults.MediumContainerHeight),
                    contentPadding = ButtonDefaults.contentPaddingFor(ButtonDefaults.MediumContainerHeight),
                ) {
                    Text(stringResource(R.string.stop_sharing), style = MaterialTheme.typography.titleMedium)
                }
            }
        }
    }
}

@Composable
private fun MorphingBadge(phase: HeroPhase, state: TetherState) {
    val motion = MaterialTheme.motionScheme
    val colors = MaterialTheme.colorScheme
    val morph = remember { Morph(MaterialShapes.Circle, MaterialShapes.Cookie9Sided) }
    val progress by animateFloatAsState(
        targetValue = if (phase == HeroPhase.Idle) 0f else 1f,
        animationSpec = motion.slowSpatialSpec(),
        label = "morph",
    )
    val container by animateColorAsState(
        targetValue = when (phase) {
            HeroPhase.Idle -> colors.surfaceContainerHighest
            HeroPhase.Connected -> colors.primary
            else -> colors.secondaryContainer
        },
        animationSpec = motion.defaultEffectsSpec(),
        label = "badge-color",
    )
    val content = when (phase) {
        HeroPhase.Connected -> colors.onPrimary
        HeroPhase.Idle -> colors.onSurfaceVariant
        else -> colors.onSecondaryContainer
    }
    val spin = rememberInfiniteTransition(label = "spin")
    val period = if (phase == HeroPhase.Connected) 8_000 else 20_000
    val rotation by spin.animateFloat(
        initialValue = 0f,
        targetValue = 360f,
        animationSpec = infiniteRepeatable(tween(period, easing = LinearEasing)),
        label = "rotation",
    )
    val shape = remember(progress) { MorphShape(morph, progress) }

    Box(Modifier.size(148.dp), contentAlignment = Alignment.Center) {
        Box(
            Modifier
                .size(148.dp)
                .graphicsLayer { rotationZ = if (phase == HeroPhase.Idle) 0f else rotation }
                .clip(shape)
                .background(container),
        )
        if (phase == HeroPhase.Starting) {
            ContainedLoadingIndicator(Modifier.size(72.dp))
        } else {
            Icon(
                imageVector = badgeIcon(state),
                contentDescription = null,
                tint = content,
                modifier = Modifier.size(56.dp),
            )
        }
    }
}

private fun badgeIcon(state: TetherState): ImageVector = when {
    state.usb.isActive && !state.wifi.isActive -> Icons.Rounded.Usb
    state.wifi.isActive -> Icons.Rounded.WifiTethering
    else -> Icons.Rounded.Link
}

@Composable
private fun RateDisplay(icon: ImageVector, label: String, bytesPerSecond: Long) {
    val (value, unit) = splitRate(bytesPerSecond)
    Column(horizontalAlignment = Alignment.CenterHorizontally) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Icon(icon, contentDescription = null, modifier = Modifier.size(18.dp))
            Spacer(Modifier.size(4.dp))
            Text(label, style = MaterialTheme.typography.labelLarge)
        }
        Row(verticalAlignment = Alignment.Bottom) {
            Text(value, style = MaterialTheme.typography.displaySmall)
            Spacer(Modifier.size(4.dp))
            Text(unit, style = MaterialTheme.typography.titleMedium, modifier = Modifier.padding(bottom = 6.dp))
        }
    }
}

/** A Shape drawn from a [Morph] between two Material shapes at [progress]. */
private class MorphShape(private val morph: Morph, private val progress: Float) : Shape {
    private val matrix = Matrix()

    override fun createOutline(size: Size, layoutDirection: LayoutDirection, density: Density): Outline {
        val path = morph.toPath(progress)
        matrix.reset()
        matrix.scale(size.width, size.height)
        path.transform(matrix)
        return Outline.Generic(path)
    }
}

