package dev.jmbustos.rda.app

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import dev.jmbustos.rda.mobile.Mobile

@Composable
fun SettingsScreen(store: SettingsStore, modifier: Modifier = Modifier) {
    val initial = remember { store.load() }
    var url by remember { mutableStateOf(initial.signalingUrl) }
    var name by remember { mutableStateOf(initial.deviceName) }
    var token by remember { mutableStateOf(initial.deviceToken) }
    var showToken by remember { mutableStateOf(false) }
    var saved by remember { mutableStateOf(false) }

    val context = androidx.compose.ui.platform.LocalContext.current
    // ws:// solo para probar contra `rda signal-local` en la red de la casa.
    val urlError = url.isNotBlank() && !url.trim().let { it.startsWith("wss://") || it.startsWith("ws://") }

    Column(
        modifier.verticalScroll(rememberScrollState()).padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text("Señalización", style = MaterialTheme.typography.titleLarge)
        OutlinedTextField(
            value = url,
            onValueChange = { url = it; saved = false },
            label = { Text("URL del API Gateway") },
            placeholder = { Text("wss://xxxx.execute-api.us-east-1.amazonaws.com/prod") },
            isError = urlError,
            supportingText = { if (urlError) Text("Debe empezar con wss:// (o ws:// en local)") },
            singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
            modifier = Modifier.fillMaxWidth(),
        )
        OutlinedTextField(
            value = name,
            onValueChange = { name = it; saved = false },
            label = { Text("Nombre de este dispositivo") },
            placeholder = { Text("celular-jose") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
        OutlinedTextField(
            value = token,
            onValueChange = { token = it; saved = false },
            label = { Text("Token del dispositivo") },
            singleLine = true,
            visualTransformation = if (showToken) VisualTransformation.None else PasswordVisualTransformation(),
            trailingIcon = {
                TextButton(onClick = { showToken = !showToken }) { Text(if (showToken) "Ocultar" else "Ver") }
            },
            keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
            modifier = Modifier.fillMaxWidth(),
        )
        Text(
            "El token se guarda cifrado con una llave del Android Keystore. La APK no trae ningún secreto.",
            style = MaterialTheme.typography.bodySmall,
        )
        Button(
            enabled = !urlError,
            onClick = {
                val s = AppSettings(url.trim(), name.trim(), token.trim())
                store.save(s)
                saved = true
                ConnectController.configure(context, s)
                // Nunca loguear el token.
                LogStore.app("INFO", "ajustes guardados", "url=${url.trim()} nombre=${name.trim()} token=${if (token.isBlank()) "vacío" else "definido"}")
            },
        ) { Text(if (saved) "Guardado ✓" else "Guardar") }

        SshKeySection(store, name.trim())
    }
}

/**
 * La llave SSH se genera en el celular y la privada nunca sale de él (va cifrada
 * con el Keystore). La pública se autoriza en cada PC con `rda-agent -authorize`.
 */
@Composable
private fun SshKeySection(store: SettingsStore, deviceName: String) {
    val context = androidx.compose.ui.platform.LocalContext.current
    var pub by remember {
        mutableStateOf(store.loadSshKey()?.let { runCatching { Mobile.publicKeyOf(it, "rda@" + deviceName.ifBlank { "celular" }) }.getOrNull() })
    }
    var hosts by remember { mutableStateOf(store.knownHosts()) }

    Text("Llave SSH", style = MaterialTheme.typography.titleLarge)
    val p = pub
    if (p == null) {
        Text("Este celular aún no tiene llave. Genérala y luego autorízala en cada PC.", style = MaterialTheme.typography.bodyMedium)
        Button(onClick = {
            val k = Mobile.generateSSHKey("rda@" + deviceName.ifBlank { "celular" })
            store.saveSshKey(k.privatePEM)
            pub = k.publicKey
            LogStore.app("INFO", "llave SSH generada", "huella=${k.fingerprint}")
        }) { Text("Generar llave") }
        return
    }
    val cmd = "rda-agent -authorize '$p'"
    Text("Huella: ${Mobile.fingerprint(p)}", style = MaterialTheme.typography.bodySmall)
    Text("Para autorizar este celular, corre en cada PC:", style = MaterialTheme.typography.bodyMedium)
    Text(cmd, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.bodySmall)
    Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
        OutlinedButton(onClick = {
            val cm = context.getSystemService(ClipboardManager::class.java)
            cm.setPrimaryClip(ClipData.newPlainText("rda-agent -authorize", cmd))
        }) { Text("Copiar comando") }
        OutlinedButton(onClick = {
            val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, cmd)
            context.startActivity(Intent.createChooser(send, "Enviar comando al PC"))
        }) { Text("Compartir") }
    }
    Text(
        "La llave pública no es secreta: puedes mandártela por correo o chat. La privada no sale del celular.",
        style = MaterialTheme.typography.bodySmall,
    )

    if (hosts.isNotEmpty()) {
        Text("PCs recordados", style = MaterialTheme.typography.titleMedium)
        Text(
            "La llave de cada PC se guarda la primera vez. Si cambia, la app rechaza la conexión (posible suplantación). Si reinstalaste el agente, olvídala aquí.",
            style = MaterialTheme.typography.bodySmall,
        )
        hosts.forEach { (pc, key) ->
            Row(verticalAlignment = Alignment.CenterVertically) {
                Column(Modifier.weight(1f)) {
                    Text(pc)
                    Text(Mobile.fingerprint(key), style = MaterialTheme.typography.bodySmall, fontFamily = FontFamily.Monospace)
                }
                TextButton(onClick = { store.forgetHostKey(pc); hosts = store.knownHosts() }) { Text("Olvidar") }
            }
        }
    }
}
