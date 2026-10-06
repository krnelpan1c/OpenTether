package org.opentether.app.ui.activity

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.rounded.History
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialShapes
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.toShape
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import org.opentether.app.R
import org.opentether.app.core.LogEntry
import org.opentether.app.core.LogLevel

@Composable
fun ActivityScreen(entries: List<LogEntry>, contentPadding: PaddingValues) {
    if (entries.isEmpty()) {
        EmptyActivity(contentPadding)
        return
    }
    val newestFirst = remember(entries) { entries.asReversed() }
    val timeFormat = remember { SimpleDateFormat("HH:mm:ss", Locale.getDefault()) }
    LazyColumn(
        modifier = Modifier.fillMaxSize(),
        contentPadding = PaddingValues(
            start = 16.dp,
            end = 16.dp,
            top = contentPadding.calculateTopPadding() + 8.dp,
            bottom = contentPadding.calculateBottomPadding() + 16.dp,
        ),
        verticalArrangement = Arrangement.spacedBy(4.dp),
    ) {
        items(newestFirst, key = { "${it.time}-${it.message.hashCode()}-${it.level}" }) { entry ->
            LogRow(entry, timeFormat.format(Date(entry.time)))
        }
    }
}

@Composable
private fun LogRow(entry: LogEntry, time: String) {
    val colors = MaterialTheme.colorScheme
    val dot = when (entry.level) {
        LogLevel.Error -> colors.error
        LogLevel.Warn -> colors.tertiary
        LogLevel.Info -> colors.primary
        LogLevel.Debug -> colors.outline
    }
    Surface(shape = MaterialTheme.shapes.small, color = colors.surfaceContainerLow) {
        Row(Modifier.fillMaxWidth().padding(12.dp), verticalAlignment = Alignment.Top) {
            Box(Modifier.padding(top = 6.dp).size(8.dp).clip(CircleShape).background(dot))
            Column(Modifier.padding(start = 12.dp)) {
                Text(time, style = MaterialTheme.typography.labelSmall, fontFamily = FontFamily.Monospace, color = colors.onSurfaceVariant)
                Text(entry.message, style = MaterialTheme.typography.bodyMedium)
            }
        }
    }
}

@Composable
private fun EmptyActivity(contentPadding: PaddingValues) {
    Column(
        Modifier.fillMaxSize().padding(contentPadding).padding(32.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
    ) {
        Box(
            Modifier.size(120.dp).clip(MaterialShapes.Clover4Leaf.toShape()).background(MaterialTheme.colorScheme.secondaryContainer),
            contentAlignment = Alignment.Center,
        ) {
            Icon(Icons.Rounded.History, contentDescription = null, tint = MaterialTheme.colorScheme.onSecondaryContainer, modifier = Modifier.size(48.dp))
        }
        Text(
            stringResource(R.string.activity_empty),
            style = MaterialTheme.typography.bodyLarge,
            textAlign = TextAlign.Center,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.padding(top = 24.dp),
        )
    }
}
