# Разработка

Код приложения, тесты, сборочные скрипты и `yam.yaml` находятся в `source/`.
Рабочая директория для команд ниже:

```sh
cd source
```

## Проверки

Нужны Go из `go.mod`, Python 3 с curses, Node.js 24+ и Rust из `rust-toolchain.toml`.

```sh
go test -race ./...
go vet ./...
python3 -m unittest discover -v
node --test test_web_cookies.mjs
cargo test --locked --manifest-path media/upstream/Cargo.toml
```

## Сборка

> [!WARNING]
> Проверено на **Яндекс Станции Лайт 1 (YNDX-00025, `yandexmicro`)**.
> На **Яндекс Станции Мини 2 с часами (`yandexmini_2`)** подтверждены установка,
> запуск и воспроизведение. Работа на остальных моделях пока не проверена.

В `source/yam.yaml` указаны платформы `yandexmicro` (Лайт 1) и
`yandexmini_2` (Мини 2 с часами). Скрипт `source/package.py` и CI по умолчанию
собирают пакет для `yandexmicro`. Для других моделей требуется проверка
библиотек прошивки и медиаслоя на соответствующих устройствах.

Linux x86_64, Docker, Rust с rustup и Go. Станция и библиотеки прошивки для
сборки не нужны. Нативный плеер использует открытые заголовки GStreamer 1.x
и временные заглушки для линковки; на устройстве — библиотеки прошивки.

```sh
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/ytm-alice .
./build-native.sh
./media/upstream/build.sh

git clone https://github.com/nyakokitsu/yam.git ../.build/yam
git -C ../.build/yam checkout 887c9d17f370a89b2d938f5a3369076ff851dfae
cargo build --locked --release --manifest-path ../.build/yam/Cargo.toml --bin yam
python3 package.py --yam ../.build/yam/target/release/yam --profiles ../.build/yam/platforms
python3 scripts/check-package.py --yam ../.build/yam/target/release/yam --profiles ../.build/yam/platforms ../release/*.yampkg
python3 scripts/release-assets.py ../release/*.yampkg
```

После сборки бинарников и упаковщика пакет для Мини 2 с часами можно собрать
напрямую через YAM (из каталога `source/`):

```sh
../.build/yam/target/release/yam --platform yandexmini_2 --profiles ../.build/yam/platforms pack . --output ../release/ytm-alice-0.7.0-yandexmini_2.yampkg
```

`source/scripts/check-package.py` пока проверяет только пакеты `yandexmicro`;
для проверки пакета Мини 2 нужен профиль `yandexmini_2`.

Артефакты — в `release/` в корне репозитория. `yam.yaml` берёт README из корня, документацию из `docs/` и лицензии
из `licenses/`; исходный архив сохраняет структуру репозитория с каталогом `source/`.
Проверка пакета выполняет чистую установку, обновление с двух пакетов,
удаление и проверку сохранности пользовательских данных.

## Релиз

Обновите версии в `source/main.go`, `source/remote-cli.py`, `source/yam.yaml`,
`source/package.py` и заметки `docs/RELEASE.md`. Отправьте тег `vX.Y.Z`
на коммит из `main`. CI проверит версии, выполнит тесты Linux/macOS,
соберёт пакет и опубликует GitHub Release с SHA-256.

[CLI](CLI.md) · [Веб-панель](WEBUI.md) · [Участие в проекте](CONTRIBUTING.md)
