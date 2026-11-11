# Исходники плагинов

Каждый плагин находится в отдельном каталоге и содержит `plugin.json` с именем
команды, Go-пакетом и именем бинарника. Установленные исполняемые файлы лежат
в `<home>/bin/horizon-<имя>`; исходники не сканируются для запуска команд.
Адаптеры daemon устанавливаются как `agent-<имя>`.
Memory и decision встроены в ядро.

- [Manager](manager/README.md) — обязательный установщик и менеджер обновлений.
- [Agent](agent/README.md) — необязательная Telegram-интеграция; команда `telegram`.
- [Timer](agent-timer/README.md) — Markdown-расписания для daemon.
- [Browser](browser/README.md) — необязательный удалённый браузер.

```sh
horizon manager plugins list
horizon manager plugins install telegram
horizon manager plugins install browser
horizon manager plugins remove browser
```

Установка собирает выбранный плагин из установленной ревизии исходников и добавляет
недостающие настройки в `config.yaml`. Значения пользователя сохраняются.
Службы и инструменты `agent_plugins` автоматически не включаются. Удаление убирает
управляемый бинарник и его имя из `agent_plugins`, сохраняя настройки и данные.

Неуправляемые плагины можно подключать отдельным исполняемым файлом или symlink
в `bin`; менеджер не присваивает их себе. Для разработки есть Makefile,
`make install-plugins` по умолчанию устанавливает только manager и не ведёт
состояние управляемой установки. Подробности — в README Manager.
[Протокол плагинов](../docs/PLUGINS.md).
