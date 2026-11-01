package dev.jmbustos.rda.app

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp

@Composable
fun SettingsScreen(store: SettingsStore, modifier: Modifier = Modifier) {
    val initial = remember { store.load() }
    var url by remember { mutableStateOf(initial.signalingUrl) }
    var name by remember { mutableStateOf(initial.deviceName) }
    var token by remember { mutableStateOf(initial.deviceToken) }
    var showToken by remember { mutableStateOf(false) }
    var saved by remember { mutableStateOf(false) }

    val urlError = url.isNotBlank() && !url.trim().startsWith("wss://")

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
            supportingText = { if (urlError) Text("Debe empezar con wss://") },
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
                store.save(AppSettings(url, name, token))
                saved = true
                // Nunca loguear el token.
                LogStore.app("INFO", "ajustes guardados", "url=${url.trim()} nombre=${name.trim()} token=${if (token.isBlank()) "vacío" else "definido"}")
            },
        ) { Text(if (saved) "Guardado ✓" else "Guardar") }

        Text("Llave SSH", style = MaterialTheme.typography.titleLarge)
        Text(
            "Llega en la fase 4: el par de llaves se generará en el celular y podrás copiar la pública para autorizarla en cada PC.",
            style = MaterialTheme.typography.bodyMedium,
        )
    }
}
