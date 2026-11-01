VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

ANDROID_HOME ?= $(HOME)/Android/Sdk
ANDROID_NDK_HOME ?= $(lastword $(sort $(wildcard $(ANDROID_HOME)/ndk/*)))
JAVA_HOME ?= /opt/android-studio/jbr
export ANDROID_HOME ANDROID_NDK_HOME JAVA_HOME

APK_OUT := android/app/build/outputs/apk/release/app-release.apk
DIST_APK := bin/rda-$(VERSION).apk

.PHONY: build test vet fmt termux aar apk apk-debug install clean

build: ## agente y cli para esta máquina → bin/
	go build -ldflags '$(LDFLAGS)' -o bin/rda ./cli
	go build -ldflags '$(LDFLAGS)' -o bin/rda-agent ./agent

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

termux: ## cli para el celular (Android arm64, Go puro) → bin/rda-android-arm64
	CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o bin/rda-android-arm64 ./cli

aar: ## core Go → android/app/libs/core.aar (arm64 para el celular, amd64 para el emulador)
	mkdir -p android/app/libs bin/tools
	go build -o bin/tools/ golang.org/x/mobile/cmd/gobind golang.org/x/mobile/cmd/gomobile
	# gomobile invoca "go" del PATH: le damos el toolchain que pide go.mod, no el del sistema.
	PATH="$(CURDIR)/bin/tools:$$(go env GOROOT)/bin:$$PATH" gomobile bind -target=android/arm64,android/amd64 -androidapi 24 \
		-javapkg dev.jmbustos.rda \
		-ldflags '-s -w -X github.com/jmbustos1/remote-dev-access/core/mobile.version=$(VERSION)' \
		-o android/app/libs/core.aar ./core/mobile

apk: aar ## APK de release firmada → bin/rda-<versión>.apk (lista para subir a Drive)
	@test -f android/keystore.properties || { echo "falta android/keystore.properties (ver docs/android.md)"; exit 1; }
	cd android && ./gradlew --no-daemon -q assembleRelease
	mkdir -p bin && cp $(APK_OUT) $(DIST_APK)
	@echo "→ $(DIST_APK)"

apk-debug: aar
	cd android && ./gradlew --no-daemon -q assembleDebug

install: ## instala la última APK de release en el celular conectado por USB
	adb install -r $(DIST_APK)

clean:
	rm -rf bin/ android/app/libs android/app/build android/build
