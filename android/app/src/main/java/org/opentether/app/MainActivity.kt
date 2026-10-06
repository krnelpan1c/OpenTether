package org.opentether.app

import android.hardware.usb.UsbManager
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import org.opentether.app.core.ModeStatus
import org.opentether.app.core.TetherController
import org.opentether.app.service.TetherService
import org.opentether.app.ui.OpenTetherRoot
import org.opentether.app.ui.theme.OpenTetherTheme

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        setContent {
            OpenTetherTheme {
                OpenTetherRoot()
            }
        }
    }

    override fun onResume() {
        super.onResume()
        // If the desktop client is already waiting in USB accessory mode
        // (for example the system "open with" prompt was dismissed), opening
        // the app is enough to start serving it.
        val waiting = getSystemService(UsbManager::class.java)?.accessoryList
            ?.any { it.manufacturer == "OpenTether" } == true
        if (waiting && TetherController.state.value.usb.status != ModeStatus.On) {
            TetherService.start(this, TetherService.ACTION_ACCESSORY_ATTACHED)
        }
    }
}
