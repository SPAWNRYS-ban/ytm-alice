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

Для проверки публичных endpoint YouTube без входа в аккаунт:

```sh
YTM_PUBLIC_RELEASE_SMOKE=1 go test -run 'TestPublic(ReleaseSmoke|DeviceCode)' -v
```

Проверка ищет пример из #14 на латинице и кириллицей, получает и отменяет
код Google. Она не авторизует аккаунт и не входит в обычный набор CI-тестов.

## Эмуляторы Станций

Для проверок на разных прошивках используйте [локальный стенд kuyase](KUYASE.md).
Он запускает отдельные ВМ с собственными данными, ADB и веб-портами.

## Сборка

Физические проверки пока подтверждены на Лайт 1 и Мини 2 с часами.
Для 12 Linux-моделей предусмотрена проверка в kuyase; её результаты и ограничения
указаны в [матрице](KUYASE-MATRIX.md). Android/YaOS-модели не поддерживаются.

Все платформы из `source/platforms.py` используют общий набор бинарников.
Плеер выбирает ARM32/ARM64 по ELF-классу `libquasar_gstreamer.so`, а не по
`uname`: на многих Станциях ARM64-ядро с ARM32 userspace. Оба плеера включены
в каждый пакет. Мини 2 без часов использует профиль YAM `yandexmini_2`,
Лайт 2 без часов — `plum`, Станция 2 — `yandexstation_2`.

YAM v2 проверяет точное совпадение target пакета и платформы устройства.
Поэтому CI собирает нативные файлы один раз, затем упаковывает их для каждой
платформы. Общий `ytm-alice-station.tar.gz` содержит все эти пакеты; единый
`.yampkg` потребует поддержки универсального target в самом YAM.

При HTTPS приложение использует системное хранилище CA и штатные бандлы
`/system/vendor/quasar/certs/ca-certificates.crt` и
`/system/vendor/quasar/ca-certificates.crt`. Проверка сертификатов не отключается.

Linux x86_64, Docker, Rust с rustup и Go. Станция и библиотеки прошивки для
сборки не нужны. Сборка также проверяет SHA-256 и собирает QuickJS-ng 0.18.0 для
изолированного декодера подписей YouTube. Помощник статически слинкован
и не требует дополнительных библиотек прошивки. JavaScript yt-dlp/ejs 0.8.0 встроен
в Go-бинарник; отдельный Node.js/Python на колонке не нужен.
Нативный плеер использует открытые заголовки GStreamer 1.x
и временные заглушки для линковки; на устройстве — библиотеки прошивки.

```sh
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o dist/ytm-alice .
./build-native.sh
./media/upstream/build.sh

git clone https://github.com/nyakokitsu/yam.git ../.build/yam
git -C ../.build/yam checkout 887c9d17f370a89b2d938f5a3369076ff851dfae
cargo build --locked --release --manifest-path ../.build/yam/Cargo.toml --bin yam
for platform in $(python3 -c "from platforms import PLATFORMS; print(' '.join(PLATFORMS))"); do
  python3 package.py --platform "$platform" --yam ../.build/yam/target/release/yam --profiles ../.build/yam/platforms
  python3 scripts/check-package.py --platform "$platform" --yam ../.build/yam/target/release/yam --profiles ../.build/yam/platforms "../release/ytm-alice-0.7.5-$platform.yampkg"
done
python3 scripts/release-assets.py ../release/*.yampkg
```

Артефакты — в `release/` в корне репозитория. `yam.yaml` берёт README из корня, документацию из `docs/` и лицензии
из `licenses/`; исходный архив сохраняет структуру репозитория с каталогом `source/`.
Проверка пакета выполняет чистую установку, обновление с двух пакетов,
удаление и проверку сохранности пользовательских данных.

## Релиз

Обновите версии в `source/main.go`, `source/remote-cli.py`, `source/yam.yaml`,
README, примерах установки и заметках `docs/RELEASE.md`. Отправьте тег `vX.Y.Z`
на коммит из `main`. CI проверит версии, выполнит тесты Linux/macOS,
соберёт пакет и опубликует GitHub Release с SHA-256.

[CLI](CLI.md) · [Веб-панель](WEBUI.md) · [Участие в проекте](CONTRIBUTING.md)
