# Исходники плагинов

Каждый плагин хранится в отдельном каталоге `plugins/<имя>/`. Этот каталог
содержит исходники и не сканируется Horizon. Доступные плагины — `decision`, `telegram`, `memory`, `browser`, `update` и
`plugins`. Описания и зависимости находятся в [catalog.yaml](catalog.yaml).

Можно установить symlink на исполняемый файл. Для удаления достаточно удалить
`<home>/plugins/horizon-<имя>`. `init` плагины автоматически не устанавливает.
После установки выполните `horizon init` с тем же home: он добавит недостающие
настройки всех поддерживающих протокол установленных плагинов, сохранив
существующие значения. Старый сторонний плагин без шаблона будет пропущен с
сообщением. Инициализация не добавляет имя в `agent_plugins`.
Протокол описан в [docs/PLUGINS.md](../docs/PLUGINS.md).

## Decision

Плагин возвращает структурированные оценки Jev для внешних систем и скриптов:

```sh
make install-decision HORIZON_HOME=/path/to/home
horizon --home /path/to/home init
horizon --home /path/to/home decision request.json
cat request.json | horizon --home /path/to/home decision - -o result.json
```

Заполните общую секцию `decision` в `/path/to/home/config.yaml`:

```yaml
decision:
  provider:
    url: https://openrouter.ai/api/alpha/decisions
    key: YOUR_DECISION_API_KEY
  model: typesafe/jev-1.13
```

Принимается также базовый URL `https://openrouter.ai/api`. Конфигурация
загружается общим загрузчиком Horizon: основные настройки `provider` и
моделей, если они заданы, тоже должны быть валидными. Метаданные и `--help`
работают без конфигурации. `plugins.decision` не требуется.

Формат входа, выхода и коды завершения описаны в
[plugins/decision/README.md](decision/README.md). `-o` записывает ответ только
в файл; без этого флага JSON идёт в stdout. Плагин устанавливается вручную,
не создаёт сессии и не запускает цикл агента.

## Telegram

Telegram-бот с отдельным workspace и закреплённой сессией для каждого чата:

```sh
make install-telegram HORIZON_HOME=/path/to/home
horizon --home /path/to/home init
# Edit plugins.telegram.telegram in /path/to/home/config.yaml.
horizon --home /path/to/home telegram start
```

Для режима собеседника также нужен установленный `decision`:
`make install-decision HORIZON_HOME=/path/to/home`.
[Настройки, правила групп, send и управление службами](telegram/README.md).

`make install-plugins` по умолчанию устанавливает `update`, `plugins`, `decision`,
`telegram` и `memory`. Команда `make install-plugin-plugins` устанавливает только
менеджер плагинов; `make install-plugins` остаётся общей целью.
Для отдельной установки используйте `make install-decision` или
`make install-telegram`; `PLUGINS=decision` выбирает только decision.
`HORIZON_HOME` берётся из окружения или равен `~/.horizon`.
Бинарники устанавливаются с правами `0700`; ошибка сборки сохраняет
ранее установленную версию. Конфигурация и сессии не изменяются.

## Memory

`make install-memory` устанавливает отдельный плагин памяти. Включение контекста
и инструментов: `agent_plugins: [memory]` в config.yaml; настройки — plugins.memory,
которую дополняет повторный `horizon init`.
[Команды, сжатие и уровни памяти](memory/README.md).

## Browser

`make install-browser` устанавливает плагин удалённого браузера Browser Use.
Повторный `horizon init` добавляет `plugins.browser` с пустым `api_key`.
Включение инструментов: `agent_plugins: [browser]`.
[Конфигурация, команды и очистка после сбоя](browser/README.md).

## Релизная установка

В управляемой установке используйте `horizon plugins install <имя>`:
`telegram` автоматически доставляет `decision` той же версии. Повторный
`horizon init` добавит настройки, но не включит расширение агента.

[plugins](plugins/README.md) показывает каталог релиза и управляет установкой
с учётом зависимостей. [update](update/README.md) обновляет весь установленный
комплект, включая себя. Оба входят в начальную поставку и не удаляются менеджером.
Исходный каталог публикуется в release.json конкретного тега, не читается из master.
Ручные неизвестные файлы не обслуживаются менеджером.
