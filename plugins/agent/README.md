# Управляющий процесс Horizon

`horizon-agent` запускает очередь и адаптеры `agent-*`. Установка:

```sh
horizon manager plugins install agent
horizon agent service install
horizon agent validate
horizon agent start
horizon agent status
horizon agent logs
horizon agent stop
```

`start` запускает установленную службу через `launchctl` на macOS или
`systemctl --user` на Linux. `start --debug` занимает текущий терминал и
работает без файла службы. Настройки находятся в `plugins.agent`; входящие
адаптеры включаются списком `plugins.agent.adapters`. Администрирование таймеров
остаётся доступным через `horizon agent timer validate|status|unblock`.
`agent` отображается только в разделе `Installed plugins` справки `horizon -h`.
`horizon agent -h` перечисляет установленные inputs с capability `admin-cli`
в разделе `Installed inputs`, даже если они не включены в `plugins.agent.adapters`.
`horizon agent timer -h` передаёт запрос справки самому timer.
Команды `start`, `status`, `stop`, `validate`, `logs`, `recover-input`, `service` зарезервированы
для управляющего процесса. [Контракт очереди и адаптеров](../../docs/DAEMON.md).

`validate` проверяет конфигурацию управляющего процесса и настройки включённых
адаптеров без Telegram API или провайдера модели. Ошибка валидации останавливает
`start` до обращения к менеджеру служб. Установленные адаптеры старого протокола
без capability `validate` проходят только проверку метаданных.
`recover-input TASK_ID` нужен только после разбора неизвестного исхода задачи:
он разблокирует вход диалога без повторения операции.

При вызове через Horizon `start` и `stop` управляют системной службой.
Прямой `horizon-agent start` сохраняет foreground-поведение для существующих
файлов служб; вызов из Horizon определяется по `HORIZON_EXECUTABLE`.

## Службы ОС

```sh
horizon agent service install
horizon agent service remove
```

Используются абсолютный путь запущенного `horizon-agent` и нормализованный
`HORIZON_HOME`. Поэтому устанавливайте файл из постоянного установленного
бинарника, а не из временной сборки. Служба наследует окружение менеджера ОС;
переменные интерактивного shell автоматически не копируются. Секреты в файл
службы не записываются.

На Linux создаётся пользовательский unit в `$XDG_CONFIG_HOME/systemd/user`
(по умолчанию `~/.config/systemd/user`), на macOS — plist в
`~/Library/LaunchAgents`. Имя включает ID home, чтобы разные установки не
перезаписывали друг друга. `--output PATH` позволяет сохранить файл в другом
месте для ручной установки; команды `start`, `stop` и `remove` используют
стандартный путь. Существующий файл не перезаписывается.

`install` только создаёт файл; `remove` удаляет его после остановки службы.
На macOS `start` использует `launchctl bootstrap`, а `stop` — `bootout`.
На Linux используются `systemctl --user start|stop`. LaunchAgent запускается при
входе пользователя; пользовательский systemd без linger не обещает работу после
выхода. Служба перезапускается после ошибки, но не после штатного завершения.

`horizon agent logs [-n N] [-f]` выводит последние строки или следит за новыми.
На Linux читается пользовательский journal. На macOS новый plist направляет
stdout и stderr в `<home>/run/agent.stdout.log` и `agent.stderr.log`.
Старый plist без этих полей нужно обновить: `horizon agent stop`,
`horizon agent service remove`, `horizon agent service install`,
`horizon agent start`. Служебные файлы не заменяются автоматически.

Перед обновлением остановите службу через `horizon agent stop`, после обновления
запустите через `horizon agent start`. Установка файла из хода
Horizon требует `full`, поскольку каталог служб расположен вне home проекта.

Форматы: [systemd.service](https://www.freedesktop.org/software/systemd/man/latest/systemd.service.html),
[окружение systemd](https://www.freedesktop.org/software/systemd/man/latest/systemd.exec.html),
[LaunchAgents](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html).

## Переход с daemon

1. Остановите старую службу и её автозапуск, затем старый `horizon daemon`.
2. Если установлен старый manager, соберите новый менеджер из актуального checkout:
   `go build -o /tmp/horizon-manager ./plugins/manager`.
   Старый менеджер ещё не знает новое имя плагина и не может выполнить этот переход.
3. Запустите `/tmp/horizon-manager update --home /absolute/path/to/home`.
   Новый менеджер заменяет управляемый плагин `daemon` на `agent`, переносит
   `plugins.daemon` в `plugins.agent` с сохранением настроек, удаляет старый
   управляемый бинарник и сохраняет новый набор в `.manager/state.json`.
   Изменения применяются транзакционно; конфликт двух секций конфигурации или
   существующий неуправляемый `horizon-agent` останавливает обновление.
4. Создайте новую службу через `horizon agent service install` и запустите её
   через `horizon agent start`.

При ручной установке установите новый бинарник, переименуйте секцию конфигурации
и уберите старый бинарник самостоятельно после остановки. Для переходного
периода старое `plugins.daemon` читается, если `plugins.agent` отсутствует.
Каталог очереди `<home>/daemon`, socket и постоянные lock-файлы сохраняют пути:
это позволяет использовать существующую очередь и исключает второй процесс
из-за смены имени блокировки. История сессий не переписывается.
