package org.opentether.app

import android.app.Application
import org.opentether.app.core.Core
import org.opentether.app.service.Notifications

class OpenTetherApp : Application() {
    override fun onCreate() {
        super.onCreate()
        Notifications.createChannel(this)
        Core.init(this)
    }
}
