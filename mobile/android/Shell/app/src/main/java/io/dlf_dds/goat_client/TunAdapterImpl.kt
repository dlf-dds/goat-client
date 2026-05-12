package io.dlf_dds.goat_client

import android.net.VpnService
import android.util.Log
import io.dlf_dds.goat_client.gomobile.goatclient.IFaceDiscover
import io.dlf_dds.goat_client.gomobile.goatclient.NetworkChangeListener
import io.dlf_dds.goat_client.gomobile.goatclient.TunAdapter
import java.net.NetworkInterface

/**
 * Bridges [VpnService.Builder] + [VpnService.protect] across the
 * gomobile boundary into the Go-side wg-cp0 engine.
 *
 * Lifecycle: created and held by [GoatVpnService]; destroyed when
 * the service is torn down. The same instance implements
 * [TunAdapter], [IFaceDiscover], and [NetworkChangeListener] —
 * [GoatClient.getOrCreate] passes it three times for clarity.
 *
 * Three responsibilities map to the three Go-side interface slots:
 *
 *  - [configureInterface] (TunAdapter): build a VPN tunnel via
 *    VpnService.Builder using the bundle-derived address/MTU/DNS/routes,
 *    return the resulting tun fd to Go. Go then attaches its WG engine
 *    to that fd via [tun.CreateUnmonitoredTUNFromFD].
 *
 *  - [protectSocket] (TunAdapter): mark Go-engine sockets so they
 *    travel over the underlying network instead of looping through
 *    the VPN. Without this the outer wg-cp0 socket would try to
 *    encrypt itself, which deadlocks instantly.
 *
 *  - [iFaces] (IFaceDiscover): enumerate local interfaces so Go can
 *    pick a binding. Returns "name=ip;name=ip;…" — Go's android
 *    facade parses this format.
 *
 * Threading: VpnService method calls happen from arbitrary engine
 * goroutines; we rely on android.net.VpnService being thread-safe for
 * the operations we use (protect/Builder are documented as such).
 */
class TunAdapterImpl(
    @Volatile private var service: VpnService?,
) : TunAdapter, IFaceDiscover, NetworkChangeListener {

    /**
     * Late-binds the [VpnService] backing this adapter. Called by
     * [GoatVpnService.onStartCommand] when the service comes up after
     * [MainActivity] has already created the process-singleton
     * [GoatClient] with a noop adapter for its short-lived
     * importBundle / tunnelStatus calls. Without this seam, the
     * singleton would stay wired to the noop and Connect would fail
     * with "called from no-op adapter" the first time the engine tried
     * to build the VPN tunnel.
     */
    @Synchronized
    fun attachService(svc: VpnService) {
        service = svc
    }

    /** Called by [GoatVpnService.onDestroy] so we don't leak the service. */
    @Synchronized
    fun detachService() {
        service = null
    }

    /**
     * Called by the Go engine after the bundle is parsed; we build
     * the system VPN tunnel and return the tun fd.
     *
     * dns / searchDomains / routes are semicolon-separated per the
     * netbird convention preserved in the SDK contract. address is
     * "ip/prefix" (e.g. "10.64.0.5/32").
     */
    override fun configureInterface(
        address: String,
        mtu: Long,
        dns: String,
        searchDomains: String,
        routes: String,
    ): Long {
        val svc = service ?: error(
            "TunAdapterImpl.configureInterface called from no-op adapter; " +
                "this should only be invoked from a running GoatVpnService"
        )

        val builder = svc.Builder()
            .setMtu(mtu.toInt())
            .setSession("goat-client wg-cp0")
            .setBlocking(true)

        parseCidr(address)?.let { (ip, prefix) ->
            builder.addAddress(ip, prefix)
        }

        for (route in routes.split(";").filter { it.isNotBlank() }) {
            parseCidr(route)?.let { (ip, prefix) ->
                builder.addRoute(ip, prefix)
            }
        }

        for (resolver in dns.split(";").filter { it.isNotBlank() }) {
            builder.addDnsServer(resolver)
        }

        for (domain in searchDomains.split(";").filter { it.isNotBlank() }) {
            builder.addSearchDomain(domain)
        }

        val pfd = builder.establish()
            ?: throw IllegalStateException("VpnService.Builder.establish returned null")
        // detachFd hands ownership to Go; Kotlin must NOT close the fd.
        return pfd.detachFd().toLong()
    }

    override fun updateAddr(address: String) {
        // No-op on Android — VpnService.Builder cannot be reconfigured
        // in place. The Go engine handles re-anchoring by tearing down
        // and rebuilding the tunnel via [configureInterface].
    }

    override fun protectSocket(fd: Int): Boolean {
        val svc = service ?: return false
        return svc.protect(fd)
    }

    override fun iFaces(): String {
        val out = StringBuilder()
        for (iface in NetworkInterface.getNetworkInterfaces()) {
            val name = iface.name ?: continue
            for (addr in iface.inetAddresses) {
                val host = addr.hostAddress ?: continue
                if (out.isNotEmpty()) out.append(';')
                out.append(name).append('=').append(host)
            }
        }
        return out.toString()
    }

    override fun onNetworkChanged(networkType: String) {
        Log.i(TAG, "network changed: $networkType")
        // The Go engine is notified directly through the listener
        // hand-off; this Kotlin-side hook is for telemetry / UI only.
    }

    companion object {
        private const val TAG = "GoatTunAdapter"

        private fun parseCidr(s: String): Pair<String, Int>? {
            val parts = s.split('/')
            if (parts.size != 2) return null
            val prefix = parts[1].toIntOrNull() ?: return null
            return parts[0] to prefix
        }
    }
}
