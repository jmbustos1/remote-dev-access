package dev.jmbustos.rda.app

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

data class AppSettings(
    val signalingUrl: String = "",
    val deviceName: String = "",
    val deviceToken: String = "",
)

/**
 * Ajustes de la app. URL y nombre van en claro (no son secretos); el token se
 * cifra con AES-GCM usando una llave que vive en el Android Keystore y nunca
 * sale del hardware del teléfono.
 */
class SettingsStore(context: Context) {
    private val prefs = context.getSharedPreferences("settings", Context.MODE_PRIVATE)

    fun load() = AppSettings(
        signalingUrl = prefs.getString(KEY_URL, "") ?: "",
        deviceName = prefs.getString(KEY_NAME, "") ?: "",
        deviceToken = prefs.getString(KEY_TOKEN, null)?.let(::decrypt) ?: "",
    )

    fun save(s: AppSettings) {
        prefs.edit()
            .putString(KEY_URL, s.signalingUrl.trim())
            .putString(KEY_NAME, s.deviceName.trim())
            .putString(KEY_TOKEN, if (s.deviceToken.isEmpty()) null else encrypt(s.deviceToken.trim()))
            .apply()
    }

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        (ks.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        val gen = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        gen.init(
            KeyGenParameterSpec.Builder(KEY_ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setKeySize(256)
                .build()
        )
        return gen.generateKey()
    }

    // Formato guardado: base64(iv):base64(texto cifrado + tag)
    private fun encrypt(plain: String): String {
        val cipher = Cipher.getInstance(TRANSFORMATION).apply { init(Cipher.ENCRYPT_MODE, key()) }
        val ct = cipher.doFinal(plain.toByteArray())
        return b64(cipher.iv) + ":" + b64(ct)
    }

    private fun decrypt(stored: String): String? = runCatching {
        val (iv, ct) = stored.split(":", limit = 2)
        val cipher = Cipher.getInstance(TRANSFORMATION)
        cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, unb64(iv)))
        String(cipher.doFinal(unb64(ct)))
    }.onFailure {
        LogStore.app("WARN", "no se pudo descifrar el token guardado", "err=${it.javaClass.simpleName}")
    }.getOrNull()

    private fun b64(b: ByteArray) = Base64.encodeToString(b, Base64.NO_WRAP)
    private fun unb64(s: String) = Base64.decode(s, Base64.NO_WRAP)

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val KEY_ALIAS = "rda-settings"
        const val TRANSFORMATION = "AES/GCM/NoPadding"
        const val KEY_URL = "signaling_url"
        const val KEY_NAME = "device_name"
        const val KEY_TOKEN = "device_token"
    }
}
