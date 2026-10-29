# ytm-alice 0.7.0

YouTube Music для Яндекс Станций.

> [!WARNING]
> Проверено на **Яндекс Станции Лайт 1 (YNDX-00025, `yandexmicro`)**.
> На **Яндекс Станции Мини 2 с часами (`yandexmini_2`)** подтверждены установка,
> запуск и воспроизведение. Работа на остальных моделях пока не проверена.

Установите `.yampkg` через YAM v2:

Выберите пакет для своей модели: `yandexmicro` — Лайт 1,
`yandexmini_2` — Мини 2 с часами. Ниже приведён пример для Лайт 1;
для Мини 2 используйте файл `ytm-alice-0.7.0-yandexmini_2.yampkg`.
Если его нет в релизе, соберите его по [инструкции](BUILDING.md).

```sh
yam install --yes ytm-alice-0.7.0-yandexmicro.yampkg
/data/yam/bin/ytm-alice daemon install
```
