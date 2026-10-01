# Исходники плагинов

Каждый плагин хранится в отдельном каталоге `plugins/<имя>/`. Этот каталог
содержит исходники и не сканируется Horizon. Для примера:

```sh
go build -o /tmp/horizon-example ./plugins/example
horizon --home /path/to/home init
cp /tmp/horizon-example /path/to/home/plugins/horizon-example
chmod 700 /path/to/home/plugins/horizon-example
horizon --home /path/to/home example greet Ada
```

Можно установить symlink на исполняемый файл. Для удаления достаточно удалить
`<home>/plugins/horizon-<имя>`. `init` пример автоматически не устанавливает.
Протокол описан в [docs/PLUGINS.md](../docs/PLUGINS.md).

## Decision

Плагин возвращает структурированные оценки Jev для внешних систем и скриптов:

```sh
go build -o /tmp/horizon-decision ./plugins/decision
horizon --home /path/to/home init
cp /tmp/horizon-decision /path/to/home/plugins/horizon-decision
chmod 700 /path/to/home/plugins/horizon-decision
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
