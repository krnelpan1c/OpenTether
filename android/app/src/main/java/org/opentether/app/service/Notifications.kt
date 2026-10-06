package org.opentether.app.service

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import androidx.core.app.NotificationCompat
import org.opentether.app.MainActivity
import org.opentether.app.R
import org.opentether.app.core.TetherState
import org.opentether.app.util.formatRate

object Notifications {
    const val CHANNEL = "sharing"
    const val ID = 1

    fun createChannel(context: Context) {
        val channel = NotificationChannel(
            CHANNEL,
            context.getString(R.string.notif_channel),
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = context.getString(R.string.notif_channel_desc)
            setShowBadge(false)
        }
        context.getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
    }

    fun build(context: Context, state: TetherState): Notification {
        val title = if (state.connected) {
            context.resources.getQuantityString(R.plurals.notif_title_sharing, state.stats.sessions, state.stats.sessions)
        } else {
            context.getString(R.string.notif_title_waiting)
        }
        val modes = buildList {
            if (state.usb.isActive) add(context.getString(R.string.mode_usb))
            if (state.wifi.isActive) add(context.getString(R.string.mode_wifi))
        }.joinToString(" + ")
        val text = if (state.connected) {
            "$modes · ↑ ${formatRate(state.rateUp)}  ↓ ${formatRate(state.rateDown)}"
        } else {
            modes
        }
        val open = PendingIntent.getActivity(
            context, 0,
            Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
            PendingIntent.FLAG_IMMUTABLE,
        )
        val stop = PendingIntent.getService(
            context, 1,
            Intent(context, TetherService::class.java).setAction(TetherService.ACTION_STOP_ALL),
            PendingIntent.FLAG_IMMUTABLE,
        )
        return NotificationCompat.Builder(context, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_tether)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .setSilent(true)
            .setCategory(NotificationCompat.CATEGORY_SERVICE)
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .addAction(0, context.getString(R.string.notif_stop), stop)
            .build()
    }
}
