<p align="center">
  <img src="web/logo.svg" width="112" height="112" alt="ytm-alice">
</p>
<h1 align="center">ytm-alice</h1>
<p align="center">YouTube Music на Яндекс Станции: голос, веб-панель и терминал.</p>
<p align="center">
  <a href="https://github.com/SPAWNRYS-ban/ytm-alice/actions/workflows/ci.yml"><img src="https://github.com/SPAWNRYS-ban/ytm-alice/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/SPAWNRYS-ban/ytm-alice/releases/latest"><img src="https://img.shields.io/github/v/release/SPAWNRYS-ban/ytm-alice" alt="Релиз"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue" alt="MIT"></a>
</p>

Музыка играет автономно на Станции. Штатная Алиса, кнопка паузы и управление
голосом сохраняются. Веб-панель позволяет искать треки, управлять очередью,
слушать лайки и личные плейлисты. Компьютер и homelab для воспроизведения не нужны.

Поддерживается **Станция Лайт первого поколения — YNDX-00025, `yandexmicro`**.
Другие модели пока не проверены. Первый публичный выпуск — единый пакет с плеером
и медиаслоем bt-proxy; Spotify Connect и AirPlay в него не входят.

## Установка на Станцию

Нужны root-доступ по SSH, [YAM v2 от nyakokitsu](https://github.com/nyakokitsu/yam)
и доступ Станции к YouTube. Подготовка устройства описана в
[HackAlice](https://hackalice.me/). Обычная Станция без root не поддерживается.
На компьютере для установки нужны Python 3, SSH, curl и tar.

Настройте SSH alias `alice` с уже работающим входом root по ключу. Например,
в `~/.ssh/config` (подставьте свой адрес, порт и путь к ключу):

```sshconfig
Host alice
    HostName <IP_СТАНЦИИ>
    User root
    Port 22
    IdentityFile ~/.ssh/station_key
```

Проверьте `ssh alice 'yam --version'`, затем на компьютере выполните:

```sh
curl -fLO https://github.com/SPAWNRYS-ban/ytm-alice/releases/latest/download/ytm-alice-station.tar.gz
curl -fLO https://github.com/SPAWNRYS-ban/ytm-alice/releases/latest/download/SHA256SUMS
# Linux: sha256sum --ignore-missing -c SHA256SUMS
# macOS: shasum -a 256 ytm-alice-station.tar.gz — сравните с SHA256SUMS
tar -xzf ytm-alice-station.tar.gz
python3 ytm-alice-station/install.py ytm-alice-station/*.yampkg --host alice
```

Установщик ставит `local/ytm-alice`, настраивает медиаслой и автозапуск,
проверяет пакет и удаляет временный архив со Станции. Существующая очередь
и сессия сохраняются при обновлении. Отдельный `local/alice-media` заменяется
единым пакетом. Бэкапы на Станции не создаются.

Откройте **`http://IP_СТАНЦИИ`**. Адрес `http://alice.lan` работает, если вы
добавили такую DNS-запись на своём роутере. Панель использует HTTP, порт 80;
она доступна без пароля в доверенной локальной сети. Не публикуйте её в интернете.

Поиск и воспроизведение доступны без входа. Для лайков и личных плейлистов
откройте настройки панели: там есть инструкция по экспорту cookies YouTube
и выбору аккаунта/канала. Обложки загружает сам браузер. Сессия YouTube может
истечь: тогда повторите импорт. Пароль Google не требуется.
Подробности — [WEBUI.md](WEBUI.md).

Голосовые команды:

- «Алиса, поставь Sewerslvt Newlove».
- «Алиса, включи песню Daft Punk Get Lucky».
- «Алиса, включи мои лайки» — после входа в аккаунт.
- Во время воспроизведения: «Алиса, пауза», «продолжи», «следующий трек»,
  «предыдущий трек», «что сейчас играет», «стоп».

Фраза «Включаю» произносится штатным голосом Алисы. Таймеры, умный дом
и обычные вопросы обслуживает штатная Алиса; распознавание речи остаётся
облачным. Команда «включи музыку» остаётся штатной, для YouTube называйте трек.

## Установка CLI-клиента (необязательно)

Клиент для Linux/macOS требует Python 3 с `curses` и curl. Управляет Станцией
по тому же HTTP API; SSH для обычных музыкальных команд не нужен.

```sh
curl -fsSL https://github.com/SPAWNRYS-ban/ytm-alice/releases/latest/download/install-client.sh -o /tmp/ytm-alice-install-client.sh
sh /tmp/ytm-alice-install-client.sh
```

Установщик проверяет SHA-256 и кладёт исполняемый файл в `~/.local/bin/ytm-alice`.
Если эта папка не входит в `PATH`, добавьте в настройки shell:

```sh
export PATH="$HOME/.local/bin:$PATH"
```

```sh
ytm-alice connect http://IP_СТАНЦИИ
ytm-alice                         # интерактивная панель
ytm-alice play "Daft Punk Get Lucky"
ytm-alice search "ambient"
ytm-alice select 2
ytm-alice pause
ytm-alice next
ytm-alice --json status
```

В панели: ↑/↓ — выбор, Enter — включить, Space — пауза, `/` — поиск,
`n`/`b` — следующий/предыдущий трек, `l` — лайки, `L` — плейлисты, `q` — выход.

Адрес сохраняется в `~/.config/ytm-alice/config.json`. Для разового подключения
есть `--url http://IP_СТАНЦИИ`, для скриптов — `YTM_ALICE_URL` и `--json`.
Старый способ управления через SSH доступен с `--ssh`.
Повторный запуск установщика обновляет клиент. Удаление —
`rm ~/.local/bin/ytm-alice`; сохранённый адрес удаляется отдельно.

## Команды для CLI на Станции

Подключитесь `ssh alice`. Исполняемый файл — `/data/yam/bin/ytm-alice`;
если YAM добавил `/data/yam/bin` в `PATH`, достаточно `ytm-alice`.
Python и десктопный клиент на самой Станции не нужны.

| Команда | Действие |
| --- | --- |
| `ytm-alice` или `ytm-alice ui` | Простая терминальная панель |
| `ytm-alice play "исполнитель трек"` | Включить по названию или ссылке |
| `ytm-alice search "запрос"` | Найти треки |
| `ytm-alice select 2` | Включить результат последнего поиска |
| `ytm-alice queue` / `ytm-alice jump 3` | Очередь / выбрать трек в ней |
| `ytm-alice pause` / `resume` / `toggle` | Пауза / продолжить / переключить |
| `ytm-alice next` / `previous` / `stop` | Следующий / предыдущий / остановить |
| `ytm-alice seek 60` | Перейти к 60-й секунде |
| `ytm-alice status` / `ytm-alice --json status` | Состояние в текстовом / JSON-формате |
| `ytm-alice liked` / `play "мои лайки"` | Показать / включить лайки |
| `ytm-alice playlists` / `playlist ID` | Плейлисты / включить выбранный |
| `ytm-alice auth status` / `auth check` | Сохранённая сессия / проверка входа |
| `ytm-alice logout` | Удалить сессию со Станции |
| `ytm-alice announce-on` / `announce-off` | Включить / отключить «Включаю» |
| `ytm-alice web status` | Адреса веб-панели |
| `ytm-alice daemon restart` | Перезапустить сервис |
| `ytm-alice --help` / `--version` | Справка / версия |

Для ручной установки `.yampkg` на самой Станции:

```sh
yam install --yes /tmp/ytm-alice-0.7.0-yandexmicro.yampkg
/data/yam/bin/ytm-alice daemon install
yam doctor
```

При обновлении пользуйтесь `install.py` с компьютера: он останавливает старые
процессы перед заменой файлов. Удаление: сначала `ytm-alice daemon uninstall`,
затем `yam remove local/ytm-alice` и перезагрузка Станции.

### Ошибки, предложения и разработка

Создайте [issue по шаблону](https://github.com/SPAWNRYS-ban/ytm-alice/issues/new/choose).
Для ошибки нужны версия, модель, шаги, ожидаемый и фактический результат;
для проблемы с треком — название/ссылка и приблизительное время сбоя.
Как собрать безопасную диагностику — [CONTRIBUTING.md](CONTRIBUTING.md).

Сборка из исходников, структура проекта и выпуск релизов — [docs/BUILDING.md](docs/BUILDING.md).
CI проверяет Go, Python, браузерный разбор cookies и Rust, собирает `.yampkg`
без доступа к устройству и проверяет установку/удаление в изолированном каталоге.
Тег `vX.Y.Z` публикует прошедший эти проверки релиз с контрольными суммами.

Наш код — [MIT](LICENSE). Встроенный bt-proxy — CC BY 4.0 с сохранением
авторства; полный перечень — [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
Независимый проект, не связанный с Яндексом, YouTube или Google.
