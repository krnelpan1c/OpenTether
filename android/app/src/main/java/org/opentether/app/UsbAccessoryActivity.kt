package org.opentether.app

import android.app.Activity
import android.hardware.usb.UsbManager
import android.os.Bundle
import org.opentether.app.service.TetherService

/**
 * Invisible entry point for USB_ACCESSORY_ATTACHED. Android launches it when
 * the desktop client switches the phone into accessory mode and the user
 * agrees to open OpenTether; it hands over to the service and closes.
 */
class UsbAccessoryActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        if (intent?.action == UsbManager.ACTION_USB_ACCESSORY_ATTACHED) {
            TetherService.start(this, TetherService.ACTION_ACCESSORY_ATTACHED)
        }
        finish()
    }
}
