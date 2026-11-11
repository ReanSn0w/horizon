# Входной адаптер Telegram

`agent-telegram` получает обновления Telegram long polling. Он сохраняет
ожидающее сообщение и подтверждает offset после долговечной записи daemon.
Уточнения попадают в текущий ход на ближайшей безопасной границе или в следующий
ход того же диалога. Для каждого чата daemon использует свою привязку workspace
и сессии. Миграция группы в супергруппу сохраняет эту привязку.

Установите `daemon`, `telegram` и `agent:telegram` через
`horizon manager plugins install <имя>`. В `config.yaml`:

```yaml
agent_plugins: [telegram]
agent_communication: [telegram]
plugins:
  telegram:
    bot_token: ""
    owner_user_id: 0
  daemon:
    adapters: [telegram]
    max_active_adapter_dialogs: 2
  agent-telegram:
    private_access: write
    group_access: read
    group_defaults:
      owner_only: true
      response_mode: mention
    groups: {}
    history_messages: 20
    idle_compact_after: 12h
```

Заполните параметры бота и запустите `horizon daemon start`. Адаптер
перечитывает свои настройки после перезапуска. `workspace_dir` по умолчанию
равен `<home>/workspaces/telegram`; явно заданный относительный путь
разрешается от `<home>`.

Личные сообщения принимаются только от владельца. Настройки отдельной группы
задаются в `groups` по отрицательному ID, который показывает
`horizon telegram list`. `owner_only` ограничивает инициаторов хода. Текст
остальных участников может попасть в ограниченный контекст этого же чата.

В `mention` триггером служит явное упоминание `@username` бота. В
`conversation` каждое подходящее текстовое сообщение входит в новый или
активный ход. Учитывайте расход модели при большом потоке таких сообщений.
Доступ бота к групповым сообщениям зависит от его настроек Telegram.

Модель сама решает, отправить сообщение, несколько сообщений или промолчать.
Молчание допустимо и при упоминании. Итог хода и диагностика остаются в
Horizon. Пока ход активен, Telegram может показывать typing. После простоя
daemon может сжать достаточно большой checkpoint, а новый вход отложит или
отменит обслуживание. Значение `idle_compact_after: 0s` выключает его.

Адаптер сохраняет диагностическое сообщение последней ошибки в
`<home>/gateway/telegram-adapter.error`. Подтверждённые внешние отправки и
миграция прежней установки описаны в [README расширения](../telegram/README.md).
