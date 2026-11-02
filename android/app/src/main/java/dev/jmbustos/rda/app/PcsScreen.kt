package dev.jmbustos.rda.app

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp

@Composable
fun PcsScreen(modifier: Modifier = Modifier) {
    val st by ConnectController.state.collectAsState()
    var msg by rememberSaveable { mutableStateOf("") }

    Column(
        modifier.verticalScroll(rememberScrollState()).padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("Señalización: ${signalText(st)}", modifier = Modifier.weight(1f),
                color = if (st.signal == "connected") MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurface)
            if (st.configured) TextButton(onClick = { ConnectController.refresh() }) { Text("Actualizar") }
        }
        if (!st.configured) {
            Text("Configura la URL, el nombre y el token en Ajustes.")
            return@Column
        }

        if (st.phase != SessionPhase.None) {
            Card(Modifier.fillMaxWidth()) {
                Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(st.pc, style = MaterialTheme.typography.titleLarge)
                    Text(
                        when (st.phase) {
                            SessionPhase.Connecting -> "Conectando: señalización → candidatos → hole punching…"
                            SessionPhase.Connected -> "✓ Conectado directo con ${st.peer} — RTT ${st.rttMs} ms"
                            SessionPhase.Lost -> "✗ Conexión perdida, reintentando…"
                            SessionPhase.None -> ""
                        },
                        fontWeight = FontWeight.Bold,
                        color = if (st.phase == SessionPhase.Lost) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.primary,
                    )
                    st.chat.takeLast(12).forEach {
                        Text((if (it.mine) "yo> " else "${st.pc}> ") + it.text, fontFamily = FontFamily.Monospace)
                    }
                    if (st.phase == SessionPhase.Connected || st.phase == SessionPhase.Lost) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            OutlinedTextField(
                                value = msg, onValueChange = { msg = it },
                                label = { Text("Mensaje (el agente responde con eco)") },
                                singleLine = true, modifier = Modifier.weight(1f),
                            )
                            TextButton(enabled = msg.isNotBlank() && st.phase == SessionPhase.Connected,
                                onClick = { ConnectController.send(msg); msg = "" }) { Text("Enviar") }
                        }
                    }
                    OutlinedButton(onClick = { ConnectController.disconnect() }) { Text("Desconectar") }
                }
            }
        }

        st.error?.let { Text("Error: $it", color = MaterialTheme.colorScheme.error) }

        Text("PCs", style = MaterialTheme.typography.titleMedium)
        val pcs = st.devices.filter { it.kind == "pc" }
        if (pcs.isEmpty()) Text("No hay PCs registrados (o aún no llega la lista).")
        pcs.forEach { d ->
            Card(Modifier.fillMaxWidth()) {
                Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text(d.name, style = MaterialTheme.typography.titleMedium)
                        Text(if (d.online) "en línea" else "desconectado",
                            color = if (d.online) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outline)
                    }
                    Button(
                        enabled = d.online && st.signal == "connected" && st.phase != SessionPhase.Connecting,
                        onClick = { ConnectController.connect(d.name) },
                    ) { Text(if (st.pc == d.name && st.phase != SessionPhase.None) "Reconectar" else "Conectar") }
                }
            }
        }
    }
}

private fun signalText(st: ConnectState) = when {
    st.signal == "connected" -> "conectada ✓"
    st.signal == "connecting" -> "conectando…"
    st.signal.startsWith("disconnected") -> "desconectada (${st.signal.removePrefix("disconnected: ")})"
    else -> st.signal
}
