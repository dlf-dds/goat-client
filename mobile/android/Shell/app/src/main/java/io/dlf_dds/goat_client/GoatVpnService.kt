package io.dlf_dds.goat_client

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import android.util.Log
import io.dlf_dds.goat_client.gomobile.goatclient.DnsReadyListener
import io.dlf_dds.goat_client.gomobile.goatclient.Goatclient
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch

/**
 * The system-level VPN tunnel for goat-client.
 *
 * Started by [MainActivity] via `startForegroundService(ACTION_START)`
 * after [VpnService.prepare] consent. Lifecycle:
 *
 *   1. onCreate: create notification channel, build the VPN tunnel
 *      (deferred to first ACTION_START since the gomobile engine
 *      drives the build via [TunAdapterImpl.configureInterface]).
 *   2. onStartCommand(ACTION_START): read the operator-selected mode
 *      from [ModeStore], dispatch to the corresponding start path:
 *        • `wg-cp0-only` — today's existing GoatClient.run path
 *          (wg-cp0 outer only).
 *        • `netbird-only` — refused cleanly until Worker A's 76N
 *          InnerMesh library lands. No tunnel up.
 *        • `combined` — wg-cp0 outer via today's path; inner-mesh
 *          half is dormant pending 76N. Notification text surfaces
 *          the partial state.
 *   3. onStartCommand(ACTION_STOP): call [Client.stop], cancel the
 *      coroutine scope, stopForeground + stopSelf.
 *
 * Mode changes from the activity trigger ACTION_STOP + ACTION_START —
 * the new mode is read here on the next start.
 *
 * The persistent notification is required by Android 8+ for any
 * foreground service. We attach a tap-to-open intent that re-opens
 * the activity so the user can disconnect from there.
 */
class GoatVpnService : VpnService() {

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var engineJob: Job? = null
    private var activeMode: OperatingMode = OperatingMode.DEFAULT

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_START -> startEngine()
            ACTION_STOP  -> stopEngine()
            else         -> Log.w(TAG, "onStartCommand: unknown action ${intent?.action}")
        }
        return START_STICKY
    }

    private fun startEngine() {
        if (engineJob?.isActive == true) {
            Log.i(TAG, "engine already running")
            return
        }
        activeMode = ModeStore.read(applicationContext)
        Log.i(TAG, "startEngine: mode=${activeMode.raw}")
        ensureNotificationChannel()
        startForegroundCompat(buildNotification(notificationTextForMode(activeMode)))

        // netbird-only mode needs Worker A's InnerMesh library. Refuse
        // cleanly rather than silently fall back — the user picked
        // "inner only" and would not expect wg-cp0 to come up. Surface
        // the reason in the notification and stop the service so the
        // user sees the system VPN indicator drop.
        if (activeMode == OperatingMode.NETBIRD_ONLY) {
            Log.w(TAG, "netbird-only mode requires Block 76N InnerMesh library — not yet runtime-supported on this build")
            updateNotification("Inner-mesh-only mode pending Block 76N. Switch to wg-cp0-only or wait for next build.")
            // Don't stopSelf immediately — leave the foreground
            // notification visible so the user reads the reason. The
            // service stops when the user taps Disconnect.
            return
        }
        if (activeMode == OperatingMode.COMBINED) {
            Log.i(TAG, "combined mode: starting wg-cp0 outer; inner mesh dormant pending 76N")
        }

        // Attach this service to the process-wide TunAdapter so that the
        // engine's subsequent configureInterface / protectSocket calls
        // route through VpnService.Builder / VpnService.protect instead
        // of the noop fallback the activity creates during importBundle.
        val client = GoatClient.acquireForVpnService(applicationContext, this)

        engineJob = scope.launch {
            try {
                val files = PlatformFilesImpl(applicationContext)
                val dnsList = Goatclient.newDNSList()
                val envList = Goatclient.newEnvList()
                val dnsReady = DnsReadyListener { /* engine signal — UI polls separately */ }
                client.run(files, dnsList, dnsReady, envList)
            } catch (t: Throwable) {
                // Run() returning an error is the expected scaffolding
                // path until Track A's internal/tunnel converges; log
                // and let the foreground service stay up so the user
                // can read the status before tapping disconnect.
                Log.w(TAG, "engine run returned: ${t.message}")
            }
        }
    }

    private fun stopEngine() {
        try {
            // GoatClient is the singleton holder; .stop() is idempotent.
            GoatClient.get(applicationContext).stop()
        } catch (t: Throwable) {
            Log.w(TAG, "engine stop failed", t)
        }
        engineJob?.cancel()
        engineJob = null
        GoatClient.releaseVpnService()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            stopForeground(STOP_FOREGROUND_REMOVE)
        } else {
            @Suppress("DEPRECATION")
            stopForeground(true)
        }
        stopSelf()
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    override fun onRevoke() {
        Log.i(TAG, "VPN consent revoked by system")
        stopEngine()
        super.onRevoke()
    }

    private fun startForegroundCompat(notification: Notification) {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(
                NOTIFICATION_ID,
                notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE,
            )
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun updateNotification(text: String) {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.notify(NOTIFICATION_ID, buildNotification(text))
    }

    private fun notificationTextForMode(mode: OperatingMode): String =
        when (mode) {
            OperatingMode.WG_CP0_ONLY -> getString(R.string.vpn_notification_text_connecting)
            OperatingMode.NETBIRD_ONLY -> "Bringing up inner mesh…"
            OperatingMode.COMBINED -> "Bringing up combined tunnels…"
        }

    private fun buildNotification(text: String): Notification {
        val openActivity = PendingIntent.getActivity(
            this, 0,
            Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        return Notification.Builder(this, CHANNEL_ID)
            .setContentTitle(getString(R.string.vpn_notification_title))
            .setContentText(text)
            .setSmallIcon(android.R.drawable.ic_lock_lock)
            .setContentIntent(openActivity)
            .setOngoing(true)
            .build()
    }

    private fun ensureNotificationChannel() {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (nm.getNotificationChannel(CHANNEL_ID) != null) return
        val ch = NotificationChannel(
            CHANNEL_ID,
            getString(R.string.vpn_channel_name),
            NotificationManager.IMPORTANCE_LOW,
        ).apply {
            description = getString(R.string.vpn_channel_description)
            setShowBadge(false)
        }
        nm.createNotificationChannel(ch)
    }

    companion object {
        const val ACTION_START = "io.dlf_dds.goat_client.action.START"
        const val ACTION_STOP  = "io.dlf_dds.goat_client.action.STOP"

        private const val TAG = "GoatVpnService"
        private const val CHANNEL_ID = "goat-client-tunnel"
        private const val NOTIFICATION_ID = 1
    }
}
