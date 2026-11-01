package dev.jmbustos.rda.app

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
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
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp

@Composable
fun PunchCard() {
    val st by PunchController.state.collectAsState()
    val clipboard = LocalClipboardManager.current
    var peer by rememberSaveable { mutableStateOf("") }
    var msg by rememberSaveable { mutableStateOf("") }

    Card(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text("Hole punching", style = MaterialTheme.typography.titleLarge)
            Text(
                "1) En el PC: rda punch.  2) Aquí: Iniciar.  3) Pega la línea del PC y Conectar (si el PC tiene UPnP no hace falta pegarle la tuya).  Copia y pega sin salir de la app si puedes.",
                style = MaterialTheme.typography.bodyMedium,
            )

            if (st.phase == PunchPhase.Idle) {
                Button(onClick = { PunchController.open() }) { Text("Iniciar") }
                return@Column
            }

            Text(statusText(st), fontWeight = FontWeight.Bold, color = statusColor(st.phase))
            st.error?.let { Text(it, color = MaterialTheme.colorScheme.error) }

            if (st.candidates.isNotEmpty()) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("Tus candidatos", fontWeight = FontWeight.Bold, modifier = Modifier.weight(1f))
                    TextButton(onClick = { clipboard.setText(AnnotatedString(st.candidates)) }) { Text("Copiar línea") }
                }
                Text(st.candidatesDetail, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall)
            }

            if (st.phase == PunchPhase.Waiting || st.phase == PunchPhase.Failed) {
                OutlinedTextField(
                    value = peer,
                    onValueChange = { peer = it },
                    label = { Text("Línea de candidatos del PC") },
                    modifier = Modifier.fillMaxWidth(),
                )
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    TextButton(onClick = { clipboard.getText()?.let { peer = it.text.trim() } }) { Text("Pegar") }
                    Button(enabled = peer.isNotBlank(), onClick = { PunchController.connect(peer) }) {
                        Text(if (st.phase == PunchPhase.Failed) "Reintentar" else "Conectar")
                    }
                }
            }

            if (st.phase == PunchPhase.Connected || st.chat.isNotEmpty()) {
                st.chat.takeLast(10).forEach {
                    Text(
                        (if (it.mine) "yo> " else "pc> ") + it.text,
                        fontFamily = FontFamily.Monospace,
                        color = if (it.mine) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurface,
                    )
                }
                Row(verticalAlignment = Alignment.CenterVertically) {
                    OutlinedTextField(
                        value = msg,
                        onValueChange = { msg = it },
                        label = { Text("Mensaje") },
                        singleLine = true,
                        modifier = Modifier.weight(1f),
                    )
                    TextButton(
                        enabled = msg.isNotBlank() && st.phase == PunchPhase.Connected,
                        onClick = { PunchController.send(msg); msg = "" },
                    ) { Text("Enviar") }
                }
            }

            OutlinedButton(onClick = { PunchController.close(); peer = ""; msg = "" }) { Text("Cerrar sesión") }
        }
    }
}

private fun statusText(st: PunchState) = when (st.phase) {
    PunchPhase.Idle -> ""
    PunchPhase.Opening -> "Consultando STUN…"
    PunchPhase.Waiting -> "Esperando la dirección del otro lado (puerta abierta con keepalive)"
    PunchPhase.Punching -> "Disparando hacia ${st.peer}…"
    PunchPhase.Inbound -> "Llegan paquetes de ${st.peer}, confirmando la vuelta…"
    PunchPhase.Connected -> "✓ Conectado directo con ${st.peer} — RTT ${st.rttMs} ms"
    PunchPhase.Lost -> "✗ Conexión perdida, reintentando…"
    PunchPhase.Failed -> "✗ El disparo rápido no conectó (sigo intentando cada 5 s)"
}

@Composable
private fun statusColor(p: PunchPhase) = when (p) {
    PunchPhase.Connected -> MaterialTheme.colorScheme.primary
    PunchPhase.Lost, PunchPhase.Failed -> MaterialTheme.colorScheme.error
    else -> MaterialTheme.colorScheme.onSurface
}
