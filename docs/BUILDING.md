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
> Экспериментальная интеграция с Luna тестируется только на Лайт 1
> (`yandexmicro`). Для Мини 2 (`yandexmini_2`) проверяется сборка и установка
> пакета в тестовое окружение, работа на устройстве пока не проверена.

В `source/yam.yaml` указаны платформы `yandexmicro` (Лайт 1) и
`yandexmini_2` (Мини 2 с часами). CI собирает и проверяет пакеты для обеих
платформ. Для других моделей требуется проверка
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
for platform in yandexmicro yandexmini_2; do
  python3 package.py --platform "$platform" --yam ../.build/yam/target/release/yam --profiles ../.build/yam/platforms
  python3 scripts/check-package.py --platform "$platform" --yam ../.build/yam/target/release/yam --profiles ../.build/yam/platforms "../release/ytm-alice-0.8.0-luna.1-$platform.yampkg"
done
python3 scripts/build-luna.py --yam ../.build/yam/target/release/yam --profiles ../.build/yam/platforms
python3 scripts/release-assets.py --luna-source ../.build/luna-integration/source ../release/ytm-alice-*.yampkg
```

Артефакты — в `release/` в корне репозитория. `yam.yaml` берёт README из корня, документацию из `docs/` и лицензии
из `licenses/`; исходный архив сохраняет структуру репозитория с каталогом `source/`.
Проверка пакета выполняет чистую установку, обновление с двух пакетов,
удаление и проверку сохранности пользовательских данных.

## Релиз

Обновите версии в `source/main.go`, `source/remote-cli.py`, `source/yam.yaml`,
`source/luna/plugin.toml`,
README, примерах установки и заметках `docs/RELEASE.md`. Отправьте тег `vX.Y.Z`
на коммит из `main`. CI проверит версии, выполнит тесты Linux/macOS,
соберёт пакеты и опубликует GitHub Release с SHA-256. Теги с суффиксом,
например `v0.8.0-luna.1`, публикуются как экспериментальный pre-release без
смены Latest. Они могут указывать на коммит отдельной экспериментальной ветки;
для стабильного тега по-прежнему требуется коммит из `main`.

[CLI](CLI.md) · [Веб-панель](WEBUI.md) · [Участие в проекте](CONTRIBUTING.md)
