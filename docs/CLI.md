# CLI

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

Для экспериментальной версии 0.8.0-luna.1 сначала установите доработанную Luna
по [инструкции](LUNA.md).

Выберите пакет для своей модели: `yandexmicro` — Лайт 1,
`yandexmini_2` — Мини 2 с часами. Ниже приведён пример для Лайт 1;
для Мини 2 используйте файл `ytm-alice-0.8.0-luna.1-yandexmini_2.yampkg`.

```sh
yam install --yes /tmp/ytm-alice-0.8.0-luna.1-yandexmicro.yampkg
/data/yam/bin/ytm-alice daemon install
yam doctor
```

При обновлении пользуйтесь `install.py` с компьютера: он останавливает старые
процессы перед заменой файлов. Удаление: сначала `ytm-alice daemon uninstall`,
затем `yam remove local/ytm-alice` и перезагрузка Станции.
