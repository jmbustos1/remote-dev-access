# remote-dev-access

Terminal desde el celular hacia mis PCs detrás de NAT, vía P2P (WebRTC + hole punching) con
señalización serverless en AWS y relay de respaldo bajo demanda. Pensado para retomar sesiones de
Claude Code que viven en `tmux`.

Estado: fase 1 (STUN). Ver [PLAN.md](PLAN.md) y [docs/experimentos.md](docs/experimentos.md).

## Uso

```
make build          # bin/rda y bin/rda-agent
make test
make termux         # bin/rda-android-arm64 para el celular

./bin/rda stun -v   # ¿cómo me ve internet? + tipo de NAT
```

En el celular (Termux):

```
cp /sdcard/Download/rda-android-arm64 ~/rda && chmod +x ~/rda
~/rda stun -v
```
