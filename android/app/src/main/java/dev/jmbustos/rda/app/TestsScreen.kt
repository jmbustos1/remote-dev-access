package dev.jmbustos.rda.app

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import dev.jmbustos.rda.mobile.Mobile
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject

private data class StunUi(
    val network: String,
    val local: String,
    val nat: String,
    val rows: List<String>,
    val symmetric: Boolean,
)

@Composable
fun TestsScreen(modifier: Modifier = Modifier) {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var running by remember { mutableStateOf(false) }
    var result by remember { mutableStateOf<StunUi?>(null) }
    var error by remember { mutableStateOf<String?>(null) }

    Column(
        modifier.verticalScroll(rememberScrollState()).padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Card(Modifier.fillMaxWidth()) {
            Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("STUN", style = MaterialTheme.typography.titleLarge)
                Text(
                    "Pregunta a Google y Cloudflare cómo te ve internet y compara los puertos para saber si el hole punching funcionará en esta red.",
                    style = MaterialTheme.typography.bodyMedium,
                )
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Button(
                        enabled = !running,
                        onClick = {
                            running = true
                            error = null
                            scope.launch {
                                val network = networkType(context)
                                LogStore.app("INFO", "probando STUN", "red=$network")
                                runCatching {
                                    withContext(Dispatchers.IO) { Mobile.probeSTUN(2000) }
                                }.onSuccess { json ->
                                    result = parse(network, json)
                                }.onFailure {
                                    error = it.message
                                    LogStore.app("ERROR", "STUN falló", "err=${it.message}")
                                }
                                running = false
                            }
                        },
                    ) { Text("Probar STUN") }
                    if (running) {
                        Spacer(Modifier.width(16.dp))
                        CircularProgressIndicator(Modifier.size(24.dp))
                    }
                }
                error?.let { Text("Error: $it", color = MaterialTheme.colorScheme.error) }
                result?.let { r ->
                    Text("Red: ${r.network}")
                    Text("Socket local: ${r.local}", fontFamily = FontFamily.Monospace)
                    r.rows.forEach { Text(it, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall) }
                    Text(
                        "NAT: ${r.nat}",
                        style = MaterialTheme.typography.titleMedium,
                        color = if (r.symmetric) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.primary,
                    )
                }
            }
        }
        PunchCard()
        Text("Próxima prueba: señalización (fase 3).", style = MaterialTheme.typography.bodySmall)
    }
}

private fun parse(network: String, json: String): StunUi {
    val o = JSONObject(json)
    val results = o.getJSONArray("results")
    val rows = (0 until results.length()).map { i ->
        val r = results.getJSONObject(i)
        val server = r.getString("server")
        if (r.has("error")) "✗ $server\n   ${r.getString("error")}"
        else "✓ $server\n   ${r.getString("mapped")} (${r.getLong("rtt_ms")} ms)"
    }
    val nat = o.getString("nat")
    return StunUi(network, o.getString("local"), nat, rows, nat.startsWith("NAT simétrico"))
}

private fun networkType(context: Context): String {
    val cm = context.getSystemService(ConnectivityManager::class.java)
    val caps = cm.getNetworkCapabilities(cm.activeNetwork) ?: return "sin red"
    return when {
        caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) -> "wifi"
        caps.hasTransport(NetworkCapabilities.TRANSPORT_CELLULAR) -> "datos móviles"
        caps.hasTransport(NetworkCapabilities.TRANSPORT_VPN) -> "vpn"
        caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) -> "ethernet"
        else -> "otra"
    }
}
