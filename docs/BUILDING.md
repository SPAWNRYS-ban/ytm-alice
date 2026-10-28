# Сборка и выпуск

## Структура

- `*.go` — каталог, управление, поток, MPRIS и веб-сервер Станции.
- `web/` — интерфейс, встроенный в Go-бинарник.
- `native/player.c` — ARM32-плеер с GStreamer/GLib из прошивки.
- `media/` — оболочка медиашины и исходники bt-proxy с лицензией CC BY 4.0.
- `remote-cli.py` — необязательный десктопный клиент (Python, стандартная библиотека).
- `yam.yaml`, `package.py`, `install.py` — официальный пакет YAM v2 и установка.
- `.github/workflows/ci.yml` — проверки, сборка и публикация релизов.

## Проверки

Нужны Go из `go.mod`, Python 3 с curses, Node.js 24+ и Rust из `rust-toolchain.toml`.

```sh
go test -race ./...
go vet ./...
python3 -m unittest discover -v
node --test test_web_cookies.mjs
cargo test --locked --manifest-path media/upstream/Cargo.toml
```

## Сборка пакета

Linux x86_64, Docker, Rust с rustup и Go. Сборке не нужны SSH-доступ к Станции,
аккаунты и библиотеки прошивки. Для совместимости с glibc устройства нативный
плеер собирается в Debian Buster. Старый дистрибутив используется только как
контейнер сборки, не как сервер приложения.

```sh
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/ytm-alice .
./build-native.sh
./media/upstream/build.sh
```

Нативный плеер компилируется по открытым заголовкам GStreamer 1.x. Временные
заглушки задают имена импортов `libquasar_gstreamer.so` и `libglib.so` при линковке;
в пакет они не входят. На Станции используются настоящие библиотеки прошивки.
При изменении нативного кода проверяйте его на поддерживаемой модели.

Соберите официальный YAM v2 из зафиксированной ревизии:

```sh
git clone https://github.com/nyakokitsu/yam.git .build/yam
git -C .build/yam checkout 887c9d17f370a89b2d938f5a3369076ff851dfae
cargo build --locked --release --manifest-path .build/yam/Cargo.toml --bin yam
python3 package.py --yam .build/yam/target/release/yam --profiles .build/yam/platforms
python3 scripts/check-package.py --yam .build/yam/target/release/yam --profiles .build/yam/platforms release/*.yampkg
python3 scripts/release-assets.py release/*.yampkg
```

Итоговые файлы появляются в `release/`. Контрольные суммы покрывают пакет,
архив установки, исходники и CLI. В исходный архив попадают только отслеживаемые
Git файлы, а не локальные сессии, кэши или результаты сборки.

## Публикация

В `main` CI выполняет проверки Linux/macOS и собирает кандидат релиза.
В PR выполняются те же проверки без разрешения публиковать релизы.
Для выпуска обновите версии в `main.go`, `remote-cli.py`, `yam.yaml`, `package.py`
и заметки `RELEASE.md`, затем создайте и отправьте тег `vX.Y.Z`.
Тег должен указывать на коммит из `main`; CI проверяет соответствие версий.
После успешной сборки workflow публикует GitHub Release и файлы из `release/`.
Ключи устройств, SSH и доступ к аккаунтам для CI не нужны.

Установщик Станции использует существующий root SSH; резервные копии пакета
на устройстве не создаются. Установка релиза на физическую Станцию остаётся
действием пользователя и не выполняется CI.
