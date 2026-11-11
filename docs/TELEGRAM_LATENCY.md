# Диагностика задержки Telegram

Адаптер сохраняет обновление и передаёт его в очередь daemon. Состояние
`stored` подтверждает сохранение, а `included` означает, что текст появился
в истории Horizon. Между ними сообщение может ждать свободный диалог,
завершение текущего запроса модели или работу инструмента. Порядок и лимиты
описаны в [контракте daemon](DAEMON.md).

Для времени обработки хода сравните его события daemon `turn_started`,
`model_request_started`, `tool_started` и `tool_completed`. Для расхода
токенов пригодятся [события запроса модели](PROMPT_CACHE.md) в JSONL Horizon.

Telegram typing показывает активный ход. Модель может закончить его без
отправки. Если модель вызвала `telegram__send_message`, подтверждённые части
и их ID доступны в `horizon telegram read --operation-id ID`. Операция
`unknown` требует сверки с историей Telegram. Подробности —
в [README расширения](../plugins/telegram/README.md).

Ошибки polling и подтверждения ввода сохраняются в
`<home>/gateway/telegram-adapter.error`. Этот файл отражает последнюю
записанную ошибку. Доступность конкретного чата показывает
`horizon telegram list --json`.
