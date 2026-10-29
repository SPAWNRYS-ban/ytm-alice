# Встроенный медиаслой ytm-alice 0.7.2-hotfix

Общий медиаслой для Яндекс Станций. Использует
[bt-proxy из Yandex Station Extended](https://hackalice.me/recipes/bt-proxy/),
не перехватывает evdev и не отключает Алису во время музыки.

> [!WARNING]
> Проверено на **Яндекс Станции Лайт 1 (YNDX-00025, `yandexmicro`)**.
> На **Яндекс Станции Мини 2 с часами (`yandexmini_2`)** подтверждены установка,
> запуск и воспроизведение. Работа на остальных моделях пока не проверена.

MPRIS-плееры видны штатной Алисе как виртуальные Bluetooth-источники.
Кнопка ▶/Ⅱ, пауза/продолжение и переключение управляют выбранным источником;
последний заигравший плеер получает фокус. Это общий механизм: плееру нужна
поддержка MPRIS, произвольный ALSA-процесс без управления не подключается сам.

## Установка

Выберите пакет для своей модели: `yandexmicro` — Лайт 1,
`yandexmini_2` — Мини 2 с часами. Ниже приведён пример для Лайт 1;
для Мини 2 используйте файл `ytm-alice-0.7.2-hotfix-yandexmini_2.yampkg`.

```sh
yam install --yes /tmp/ytm-alice-0.7.2-hotfix-yandexmicro.yampkg
/data/yam/bin/ytm-alice daemon install
```

`install` отключает D-Bus autoactivation `org.bluez` (исходный файл сохранён
с суффиксом `.disabled`) и создаёт обратимый hook `/etc/init.d/S55zbtproxy`.
ФС возвращается в read-only. Параметры исходных Bluetooth init-скриптов,
сопряжения, Wi-Fi, SSH и защита от OTA не изменяются.

Приватная root-only шина: `unix:path=/tmp/btbus/socket`, конфигурация поставляется
пакетом. `bluetoothd`, `bluealsa`, `bluealsa-aplay` и агент сопряжения работают
на ней. `bt-proxy` пересылает настоящие устройства на системную D-Bus,
добавляет MPRIS-плееры и перезапускается супервизором при аварии.

Статус: `/data/yam/bin/alice-media status`; лог: `/tmp/bt-proxy.log`.
Удаление всего выпуска: `ytm-alice daemon uninstall`, затем
`yam remove local/ytm-alice`, перезагрузка.
Перед установкой проверяется набор имён Bluetooth-служб.

## Подключение плеера

Плеер от root регистрирует `org.mpris.MediaPlayer2.<имя>` на приватной шине,
публикует PlaybackStatus, Metadata и PropertiesChanged, реализует
Play/Pause/Next/Previous/Stop. Для стандартных реализаций:

```sh
DBUS_SYSTEM_BUS_ADDRESS=unix:path=/tmp/btbus/socket ваш-плеер
```

`ytm-alice` обнаруживает шину автоматически. Его PCM по-прежнему идёт
через штатный ALSA Master; отдельное усиление по MPRIS не добавляется.
Для Spotify/AirPlay/MPD нужны сами плееры с MPRIS, этот пакет их не устанавливает.

## Источники и лицензии

Исходный bt-proxy и конфигурация: [Marker284/yandex-station-extended](https://github.com/Marker284/yandex-station-extended/tree/c8c743b10c4f540c0a690eb7cb0cf5a92b7f336b/contrib/bt-proxy).
Ревизия `c8c743b10c4f540c0a690eb7cb0cf5a92b7f336b`; бинарник ARMv7 musl
собирается из приложенных исходников. Контрольная сумма находится в SHA256SUMS релиза.
Авторство проекта: Марк Писаренко / Yandex station extended / hackalice;
лицензия репозитория CC BY 4.0 — [UPSTREAM-LICENSE](../licenses/media/UPSTREAM-LICENSE).
Rust-код bt-proxy не изменён; изменены скрипт сборки, оболочка установки/автозапуска и pidfile шины.
Они опубликованы под MIT. Исходники bt-proxy, lock-файл и скрипт сборки приложены.
Конфигурация шины теперь размещается внутри share/local.ytm-alice/media/.
Прежний каталог var/local.alice-media сохранён для совместимости с частной
версией из двух пакетов; это данные, а не отдельный установленный пакет.
