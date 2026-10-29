<p align="center">
  <img src="source/web/logo.svg" width="112" height="112" alt="ytm-alice">
</p>
<h1 align="center">ytm-alice</h1>
<p align="center">YouTube Music на Яндекс Станции: голос, веб-панель и CLI.</p>

Поиск, очередь, лайки и плейлисты YouTube Music. Музыка играет на самой
Станции; штатная Алиса и кнопка паузы продолжают работать.

> [!WARNING]
> Проверено на **Яндекс Станции Лайт 1 (YNDX-00025, `yandexmicro`)**.
> На **Яндекс Станции Мини 2 с часами (`yandexmini_2`)** подтверждены установка,
> запуск и воспроизведение. Работа на остальных моделях пока не проверена.

## Установка на Станцию

Требуется установленный [YAM v2](https://github.com/nyakokitsu/yam).
Скачайте `.yampkg` из [последнего релиза](https://github.com/SPAWNRYS-ban/ytm-alice/releases/latest) и установите:

Выберите пакет для своей модели: `yandexmicro` — Лайт 1,
`yandexmini_2` — Мини 2 с часами. Ниже приведён пример для Лайт 1;
для Мини 2 используйте файл `ytm-alice-0.7.2-hotfix-yandexmini_2.yampkg`.

```sh
yam install --yes ytm-alice-0.7.2-hotfix-yandexmicro.yampkg
/data/yam/bin/ytm-alice daemon install
```

## Веб-панель и аккаунт

Откройте **`http://IP_СТАНЦИИ`**. Если в вашей сети настроен адрес `alice.lan`,
можно использовать **`http://alice.lan`**. Панель доступна в локальной сети без пароля.

В панели есть поиск, очередь и управление воспроизведением.
Для лайков и личных плейлистов импортируйте cookies YouTube в настройках
и выберите аккаунт. Инструкция по экспорту cookies находится там же.
Поиск и воспроизведение работают и без аккаунта.

## Голосовые команды

- «Алиса, поставь Sewerslvt Newlove».
- «Алиса, включи песню Daft Punk Get Lucky».
- «Алиса, включи мои лайки» — после подключения аккаунта.
- Во время музыки: «Алиса, пауза», «продолжи», «следующий трек»,
  «предыдущий трек», «что сейчас играет», «стоп».

## Установка CLI-клиента (необязательно)

На компьютере с Linux или macOS нужны Python 3 с `curses` и curl:

```sh
curl -fsSL https://github.com/SPAWNRYS-ban/ytm-alice/releases/latest/download/install-client.sh -o /tmp/ytm-alice-install-client.sh
sh /tmp/ytm-alice-install-client.sh
ytm-alice connect http://IP_СТАНЦИИ
ytm-alice
```

Клиент устанавливается в `~/.local/bin`; добавьте эту папку в `PATH`, если нужно.
Команда без аргументов открывает панель: Space — пауза, `/` — поиск,
`n`/`b` — следующий/предыдущий трек, `q` — выход.

## Команды CLI на Станции

В терминале Станции:

| Команда | Действие |
| --- | --- |
| `ytm-alice play "исполнитель трек"` | Включить трек |
| `ytm-alice search "запрос"` | Поиск |
| `ytm-alice select 2` | Включить второй результат поиска |
| `ytm-alice queue` | Очередь |
| `ytm-alice pause` / `ytm-alice resume` | Пауза / продолжить |
| `ytm-alice next` / `ytm-alice previous` | Следующий / предыдущий трек |
| `ytm-alice stop` | Остановить музыку |
| `ytm-alice status` | Текущий трек и состояние |
| `ytm-alice announce-on` / `ytm-alice announce-off` | Включить / отключить фразу «Включаю» |
| `ytm-alice daemon restart` | Перезапустить сервис |
| `ytm-alice --help` | Все команды |

Музыкальные команды доступны и в CLI-клиенте на компьютере.
[Подробная справка CLI](docs/CLI.md) · [Ошибки и предложения](https://github.com/SPAWNRYS-ban/ytm-alice/issues/new/choose) · [Лицензии](licenses/)
