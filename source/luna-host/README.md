# Luna для экспериментальной сборки

Upstream: https://github.com/nyakokitsu/luna

Базовый коммит: `4f769b5f184c3f83e50d7c22b16f90d6a392b602` (0.2.7).
Файл `ytm-provider.patch` содержит все изменения интеграционной сборки:
бинарные провайдеры YAM, namespace runtime API, состояние вне пакета,
отключённые автообновления и автозапуск на BusyBox init.
Это сторонняя экспериментальная сборка Luna, не официальный релиз upstream.

CI и локальная сборка используют один скрипт:

```sh
python3 source/scripts/build-luna.py --yam /path/to/yam --profiles /path/to/platforms
```

Нужны Linux x86_64, Git, Python 3.12+, Rust с rustup. Скрипт скачивает закреплённую
ревизию, применяет патч, выполняет тесты и собирает пакеты для `yandexmicro` и
`yandexmini_2`. musl toolchain проверяется по SHA-256; готовый toolchain можно
передать через `--toolchain-dir`.

Исходники и лицензия Luna включены в отдельный source-архив релиза.
