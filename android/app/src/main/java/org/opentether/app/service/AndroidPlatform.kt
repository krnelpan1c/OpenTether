package org.opentether.app.service

import android.net.DnsResolver
import android.net.Network
import android.os.CancellationSignal
import android.os.ParcelFileDescriptor
import android.util.Log
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executor
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference
import org.opentether.app.core.Core
import org.opentether.app.core.LogLevel
import org.opentether.app.core.TetherController
import org.opentether.core.mobile.Platform

/**
 * Implements the Go core's Platform interface. Called from Go threads, so
 * all state is volatile and updated by [UpstreamNetwork].
 */
class AndroidPlatform : Platform {
    /** Network the relay's sockets must use, or null if none is available. */
    @Volatile var network: Network? = null

    /** When true (mobile data mode) sockets fail rather than fall back to Wi-Fi. */
    @Volatile var bindRequired: Boolean = true

    @Volatile var ipv6: Boolean = false

    override fun bindSocket(fd: Int): Boolean {
        val n = network ?: return !bindRequired
        return try {
            // fromFd duplicates the descriptor; binding the duplicate binds the
            // same underlying socket, and closing it leaves Go's fd intact.
            ParcelFileDescriptor.fromFd(fd).use { n.bindSocket(it.fileDescriptor) }
            true
        } catch (e: Exception) {
            Log.w(Core.TAG, "bindSocket failed", e)
            false
        }
    }

    // getInstance() is deprecated on the newest SDK but is the only API back to minSdk 29.
    @Suppress("DEPRECATION")
    override fun resolveRaw(query: ByteArray): ByteArray? {
        val n = network
        if (n == null && bindRequired) return null
        val answer = AtomicReference<ByteArray?>()
        val done = CountDownLatch(1)
        val cancel = CancellationSignal()
        DnsResolver.getInstance().rawQuery(
            n, query, DnsResolver.FLAG_EMPTY, DIRECT, cancel,
            object : DnsResolver.Callback<ByteArray> {
                override fun onAnswer(result: ByteArray, rcode: Int) {
                    answer.set(result)
                    done.countDown()
                }

                override fun onError(error: DnsResolver.DnsException) {
                    done.countDown()
                }
            },
        )
        if (!done.await(6, TimeUnit.SECONDS)) cancel.cancel()
        return answer.get()
    }

    override fun hasIPv6(): Boolean = ipv6

    override fun log(level: Int, message: String) {
        val (priority, lvl) = when {
            level >= 8 -> Log.ERROR to LogLevel.Error
            level >= 4 -> Log.WARN to LogLevel.Warn
            level >= 0 -> Log.INFO to LogLevel.Info
            else -> Log.DEBUG to LogLevel.Debug
        }
        Log.println(priority, Core.TAG, message)
        if (lvl != LogLevel.Debug) TetherController.log(lvl, message)
    }

    private companion object {
        val DIRECT = Executor { it.run() }
    }
}
