# APK

## Requisitos (una vez)

- Android Studio (trae Java en `/opt/android-studio/jbr`), Android SDK y NDK en `~/Android/Sdk`.
- Go con `GOTOOLCHAIN=auto` (por defecto): `gomobile` está fijado en `go.mod` como tool y Go
  descarga solo la versión que pide.

## Compilar

```
make apk          # core.aar + APK de release firmada → bin/rda-<versión>.apk
make apk-debug    # APK de debug (firma de debug de esta máquina)
make install      # adb install de la última APK de release (celular por USB)
```

`versionCode` = número de commits (`git rev-list --count HEAD`), así cada build commiteado se puede
instalar encima del anterior. Compilar con cambios sin commitear deja la versión marcada `-dirty`.

## Firma (keystore de release)

- Keystore: `~/.keystores/remote-dev-access-release.jks` (fuera del repo).
- Contraseñas y alias: `android/keystore.properties` (en `.gitignore`).

**Respaldar ambos archivos** (gestor de contraseñas o almacenamiento cifrado). Android solo acepta
actualizar la app si la nueva APK está firmada con la misma llave: si se pierde el keystore, hay
que desinstalar la app (y perder sus ajustes) para instalar una nueva versión.

Formato de `keystore.properties`:

```
storeFile=/home/<usuario>/.keystores/remote-dev-access-release.jks
storePassword=...
keyAlias=rda
keyPassword=...
```

## Distribuir por Drive

1. `make apk` y subir `bin/rda-<versión>.apk` a Google Drive.
2. En el celular: descargar y abrir el archivo. La primera vez Android pide permitir
   "instalar apps desconocidas" para Drive o el gestor de archivos.
3. Play Protect puede avisar "app no verificada": "Instalar de todas formas".

La APK no contiene secretos (URL, token y llaves se cargan en la pestaña Ajustes), así que no
importa si el link de Drive llega a otra persona.
