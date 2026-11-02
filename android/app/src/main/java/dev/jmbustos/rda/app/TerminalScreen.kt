package dev.jmbustos.rda.app

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.FilledTonalButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView

private const val ESC = "\u001b"

/** Teclas que el teclado del celular no tiene (o esconde). */
private val extraKeys = listOf(
    "⏎" to "\r", "Esc" to ESC, "Tab" to "\t",
    "↑" to "$ESC[A", "↓" to "$ESC[B", "←" to "$ESC[D", "→" to "$ESC[C",
    "^C" to "\u0003", "^B" to "\u0002", // ^B = prefijo de tmux
    "PgUp" to "$ESC[5~", "PgDn" to "$ESC[6~",
    "|" to "|", "/" to "/", "-" to "-", "~" to "~",
)

@Composable
fun TerminalScreen() {
    val st by TerminalController.state.collectAsState()
    BackHandler { TerminalController.hide() } // atrás = minimizar; la sesión sigue

    Column(
        // safeDrawingPadding ya incluye el teclado (IME): sumarle imePadding restaba
        // su altura dos veces y la terminal quedaba de 1 fila.
        Modifier.background(Color(0xFF1E1E1E)).safeDrawingPadding(),
    ) {
        Row(Modifier.fillMaxWidth().padding(horizontal = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            TextButton(onClick = { TerminalController.hide() }) { Text("‹ PCs") }
            Text(
                "${st.pc} · " + when (st.phase) {
                    TermPhase.Opening -> "abriendo…"
                    TermPhase.Open -> "tmux"
                    TermPhase.Closed -> "cerrada"
                    TermPhase.Idle -> ""
                },
                color = Color.LightGray, maxLines = 1, overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )
            TextButton(onClick = { TerminalController.setFont(st.fontSize - 1) }) { Text("A−") }
            TextButton(onClick = { TerminalController.setFont(st.fontSize + 1) }) { Text("A+") }
        }
        if (st.phase == TermPhase.Closed) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(st.message, color = MaterialTheme.colorScheme.error, modifier = Modifier.weight(1f))
                Button(onClick = { TerminalController.show(st.pc) }) { Text("Reabrir") }
            }
        }
        AndroidView(
            factory = { ctx -> TerminalController.webView(ctx) },
            modifier = Modifier.weight(1f).fillMaxWidth(),
        )
        Row(
            Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(4.dp),
            horizontalArrangement = Arrangement.spacedBy(4.dp),
        ) {
            KeyButton("⌨") { TerminalController.showKeyboard() }
            if (st.ctrl) {
                FilledTonalButton(onClick = { TerminalController.toggleCtrl() }, contentPadding = keyPadding) { Text("Ctrl", fontSize = 13.sp) }
            } else {
                KeyButton("Ctrl") { TerminalController.toggleCtrl() }
            }
            extraKeys.forEach { (label, seq) -> KeyButton(label) { TerminalController.sendKeys(seq) } }
        }
    }
}

private val keyPadding = PaddingValues(horizontal = 10.dp, vertical = 0.dp)

@Composable
private fun KeyButton(label: String, onClick: (android.content.Context) -> Unit) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    OutlinedButton(
        onClick = { onClick(ctx) },
        contentPadding = keyPadding,
        colors = ButtonDefaults.outlinedButtonColors(contentColor = Color.White),
    ) { Text(label, fontSize = 13.sp) }
}
