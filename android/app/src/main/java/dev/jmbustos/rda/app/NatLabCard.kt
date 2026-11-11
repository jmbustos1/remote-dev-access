package dev.jmbustos.rda.app

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
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
import dev.jmbustos.rda.mobile.NATReport
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

/**
 * Experimento "natlab" (core/natlab): mide cómo reparte puertos el NAT de la red
 * actual, para saber si el camino iterativo (barrido de puertos) podría reemplazar al relay.
 * Usa solo servidores STUN públicos: no necesita el PC.
 */
@Composable
fun NatLabCard() {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    var running by remember { mutableStateOf(false) }
    var report by remember { mutableStateOf<NATReport?>(null) }
    var error by remember { mutableStateOf<String?>(null) }

    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("Experimento NAT", style = MaterialTheme.typography.titleLarge)
            Text(
                "Abre 32 sockets y le pregunta a 5 servidores STUN cómo te ven, para medir si la " +
                    "operadora cambia el puerto por destino y cómo los reparte (en orden, por bloque o al azar). " +
                    "Tarda ~5 s y usa ~10 KB. Hazlo con el wifi apagado para medir el 4G.",
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
                            LogStore.app("INFO", "experimento NAT", "red=$network")
                            runCatching {
                                withContext(Dispatchers.IO) { Mobile.measureNAT(network, 32) }
                            }.onSuccess {
                                report = it
                                LogStore.app("INFO", "experimento NAT listo", "red=$network")
                            }.onFailure {
                                error = it.message
                                LogStore.app("ERROR", "experimento NAT falló", "err=${it.message}")
                            }
                            running = false
                        }
                    },
                ) { Text("Medir NAT") }
                if (running) {
                    Spacer(Modifier.width(16.dp))
                    CircularProgressIndicator(Modifier.size(24.dp))
                }
            }
            error?.let { Text("Error: $it", color = MaterialTheme.colorScheme.error) }
            report?.let { r ->
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    OutlinedButton(onClick = {
                        val cm = context.getSystemService(ClipboardManager::class.java)
                        cm.setPrimaryClip(ClipData.newPlainText("experimento NAT", r.anon))
                    }) { Text("Copiar") }
                    OutlinedButton(onClick = {
                        val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, r.anon)
                        context.startActivity(Intent.createChooser(send, "Compartir experimento NAT"))
                    }) { Text("Compartir") }
                }
                Text(
                    "Copiar y Compartir mandan la versión anónima (tus IPs como PUB1, LOCAL).",
                    style = MaterialTheme.typography.bodySmall,
                )
                Text(
                    r.text,
                    Modifier.horizontalScroll(rememberScrollState()),
                    fontFamily = FontFamily.Monospace,
                    style = MaterialTheme.typography.bodySmall,
                    softWrap = false,
                )
            }
        }
    }
}
