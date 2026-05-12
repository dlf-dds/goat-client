package io.dlf_dds.goat_client

import android.content.Context
import android.net.VpnService
import android.os.Build
import io.dlf_dds.goat_client.gomobile.goatclient.Client as GoClient
import io.dlf_dds.goat_client.gomobile.goatclient.Goatclient

/**
 * Process-wide holder for the gomobile-bound [GoClient] instance.
 *
 * The Go-side Client is intentionally process-singleton: it owns the
 * wg-cp0 engine context, the imported-bundle invariants, and the
 * SetAndroidProtectSocketFn callback. Multiple instances would race
 * on those; the Kotlin shell creates exactly one through this object.
 *
 * Lifecycle gotcha (fixed 2026-05-12): the singleton's TunAdapter is
 * also process-wide, but its inner [VpnService] is short-lived (only
 * exists while [GoatVpnService] is running). The earlier
 * `getOrCreate(ctx, freshAdapter)` shape returned the cached client
 * AND its cached adapter — so a VpnService that arrived AFTER the
 * activity had already kicked off importBundle (which materialised
 * the singleton via [getOrCreateTransient]) couldn't get its real
 * [VpnService] into the Go-side path. The engine then called into
 * the noop adapter and failed with `configureInterface called from
 * no-op adapter`. The fix: keep one process-wide [TunAdapterImpl] and
 * let [GoatVpnService] swap its inner service in/out via attach/detach.
 */
object GoatClient {

    @Volatile
    private var instance: GoClient? = null

    @Volatile
    private var adapter: TunAdapterImpl? = null

    @Synchronized
    private fun getOrCreateClient(ctx: Context): GoClient {
        instance?.let { return it }
        val ad = TunAdapterImpl(service = null).also { adapter = it }
        val deviceName = "${Build.MANUFACTURER} ${Build.MODEL}".trim()
        val client = Goatclient.newClient(
            Build.VERSION.SDK_INT.toLong(),  // gomobile maps Go `int` → Java `long`
            deviceName,
            BuildConfigVersion.NAME,
            ad,                         // TunAdapter
            ad,                         // IFaceDiscover (same impl)
            ad,                         // NetworkChangeListener (same impl)
        )
        client.configure(PlatformFilesImpl(ctx.applicationContext))
        instance = client
        return client
    }

    /**
     * Returns the live engine, creating it on first call. Suitable for
     * activity-side calls (importBundle, tunnelStatus) that don't depend
     * on a running VpnService.
     */
    @Synchronized
    fun get(ctx: Context): GoClient = getOrCreateClient(ctx)

    /**
     * Returns the live engine with the current [VpnService] swapped into
     * its [TunAdapter] so that subsequent `Client.run` calls can build
     * the tunnel via [VpnService.Builder] and protect the outer wg
     * socket. Called by [GoatVpnService.onStartCommand].
     */
    @Synchronized
    fun acquireForVpnService(ctx: Context, svc: VpnService): GoClient {
        val client = getOrCreateClient(ctx)
        adapter?.attachService(svc)
        return client
    }

    /** Called by [GoatVpnService.onDestroy] to release the service ref. */
    @Synchronized
    fun releaseVpnService() {
        adapter?.detachService()
    }

    fun importBundle(ctx: Context, bytes: ByteArray) {
        get(ctx).importBundle(bytes)
    }

    fun tunnelStatus(ctx: Context): String =
        get(ctx).tunnelStatus
}

/**
 * Pulls the app version into a Kotlin object so [GoatClient] can pass
 * it to Go without re-importing BuildConfig (which lives in the app
 * module's namespace and ProGuard-strips poorly).
 */
object BuildConfigVersion {
    const val NAME = "0.0.1-pre"
}
