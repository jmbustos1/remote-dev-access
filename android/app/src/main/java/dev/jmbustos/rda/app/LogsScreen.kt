package dev.jmbustos.rda.app

import android.content.Intent
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.FilterChip
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

private const val ALL = "todas"
private val timeFmt = SimpleDateFormat("HH:mm:ss.SSS", Locale.US)

@Composable
fun LogsScreen(modifier: Modifier = Modifier) {
    val context = LocalContext.current
    val entries by LogStore.entries.collectAsState()
    var stage by rememberSaveable { mutableStateOf(ALL) }
    var showDebug by rememberSaveable { mutableStateOf(false) }

    val stages = listOf(ALL) + entries.map { it.stage }.distinct().sorted()
    val visible = entries.filter {
        (stage == ALL || it.stage == stage) && (showDebug || it.level != "DEBUG")
    }

    val listState = rememberLazyListState()
    LaunchedEffect(visible.size) {
        if (visible.isNotEmpty()) listState.animateScrollToItem(visible.lastIndex)
    }

    Column(modifier) {
        Row(
            Modifier.fillMaxWidth().padding(horizontal = 8.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text("Debug", style = MaterialTheme.typography.labelLarge)
            Spacer(Modifier.padding(4.dp))
            Switch(checked = showDebug, onCheckedChange = { showDebug = it })
            Spacer(Modifier.weight(1f))
            TextButton(onClick = {
                val send = Intent(Intent.ACTION_SEND).apply {
                    type = "text/plain"
                    putExtra(Intent.EXTRA_SUBJECT, "RDA logs ${BuildConfig.VERSION_NAME}")
                    putExtra(Intent.EXTRA_TEXT, LogStore.export(visible))
                }
                context.startActivity(Intent.createChooser(send, "Compartir logs"))
            }) { Text("Compartir") }
            TextButton(onClick = { LogStore.clear() }) { Text("Limpiar") }
        }
        Row(
            Modifier.horizontalScroll(rememberScrollState()).padding(horizontal = 8.dp),
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            stages.forEach { s ->
                FilterChip(selected = stage == s, onClick = { stage = s }, label = { Text(s) })
            }
        }
        HorizontalDivider()
        if (visible.isEmpty()) {
            Text("Sin logs todavía. Prueba algo en la pestaña Pruebas.", Modifier.padding(16.dp))
        }
        LazyColumn(state = listState, modifier = Modifier.weight(1f)) {
            items(visible) { e -> LogRow(e) }
        }
    }
}

@Composable
private fun LogRow(e: LogEntry) {
    val color = when (e.level) {
        "ERROR" -> MaterialTheme.colorScheme.error
        "WARN" -> Color(0xFFE0A030)
        "DEBUG" -> MaterialTheme.colorScheme.outline
        else -> MaterialTheme.colorScheme.onSurface
    }
    Column(Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp)) {
        Text(
            "${timeFmt.format(Date(e.time))} ${e.level} [${e.stage}]",
            fontFamily = FontFamily.Monospace, fontSize = 11.sp, color = MaterialTheme.colorScheme.outline,
        )
        Text(e.message, fontFamily = FontFamily.Monospace, fontSize = 13.sp, color = color)
        if (e.attrs.isNotEmpty()) {
            Text(e.attrs, fontFamily = FontFamily.Monospace, fontSize = 11.sp, color = color)
        }
    }
}
