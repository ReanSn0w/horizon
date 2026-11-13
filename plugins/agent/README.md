# Управляющий процесс Horizon

`horizon-agent` запускает очередь и адаптеры `agent-*`. Установка:

```sh
horizon manager plugins install agent
horizon agent start
horizon agent status
horizon agent stop
```

`start` работает в foreground. Настройки находятся в `plugins.agent`; входящие
адаптеры включаются списком `plugins.agent.adapters`. Администрирование таймеров
остаётся доступным через `horizon agent timer validate|status|unblock`.
Команды `start`, `status`, `stop`, `resolve-input`, `service` зарезервированы
для управляющего процесса. [Контракт очереди и адаптеров](../../docs/DAEMON.md).

## Службы ОС

```sh
horizon agent service print
horizon agent service print --platform linux
horizon agent service print --platform darwin
horizon agent service install
```

Используются абсолютный путь запущенного `horizon-agent` и нормализованный
`HORIZON_HOME`. Поэтому устанавливайте файл из постоянного установленного
бинарника, а не из временной сборки. Служба наследует окружение менеджера ОС;
переменные интерактивного shell автоматически не копируются. Секреты в файл
службы не записываются.

На Linux создаётся пользовательский unit в `$XDG_CONFIG_HOME/systemd/user`
(по умолчанию `~/.config/systemd/user`), на macOS — plist в
`~/Library/LaunchAgents`. Имя включает ID home, чтобы разные установки не
перезаписывали друг друга. `--output PATH` меняет путь установки файла.
Существующий файл не перезаписывается. Для изменения сначала остановите службу,
проверьте новый вывод `service print` и явно замените файл.

`install` только создаёт файл и печатает точные команды запуска. Для Linux это
`systemctl --user enable --now <путь>`; для macOS —
`launchctl bootstrap gui/$(id -u) <путь>`. LaunchAgent запускается при входе
пользователя; пользовательский systemd без linger не обещает работу после выхода.
Служба перезапускается после ошибки, но не после штатного завершения.

Перед обновлением остановите службу через `systemctl --user stop <имя>` или
`launchctl bootout gui/$(id -u) <путь>` — команды также печатает установщик.
После обновления запустите её тем же менеджером ОС. Установка файла из хода
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
4. Создайте новую службу через `horizon agent service install` и активируйте её.

При ручной установке установите новый бинарник, переименуйте секцию конфигурации
и уберите старый бинарник самостоятельно после остановки. Для переходного
периода старое `plugins.daemon` читается, если `plugins.agent` отсутствует.
Каталог очереди `<home>/daemon`, socket и постоянные lock-файлы сохраняют пути:
это позволяет использовать существующую очередь и исключает второй процесс
из-за смены имени блокировки. История сессий не переписывается.
