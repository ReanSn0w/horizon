# Границы Horizon

Horizon — самостоятельная CLI-утилита в корне репозитория. Один запуск
выполняет один ход модели и инструментов. Memory и decision встроены;
поддержка MCP ещё не реализована и относится к ядру Horizon.

## Ядро

Точка входа `main.go` находится в корне. Вся внутренняя логика размещена
в `internal/`: config, responses, session, buildinfo, decision, CLI, цикл хода,
исполнители инструментов, память, инструкции, bootstrap, события, код хоста
плагинов и E2E-тесты. `internal/agent` — цикл хода ядра, а не внешний agent.

Ядро отвечает за инструкции и навыки, Responses API, инструменты и режимы
доступа, лимиты и отмену, сессии, журнал вызовов, артефакты, восстановление,
сжатие контекста, память и оценку команд decision. Оно работает без плагинов.
Форматирование CLI остаётся вне хранилища и HTTP-клиентов.

`providers` хранит именованные подключения Responses API, а каждый профиль
`models` ссылается на подключение через `provider`. Роли выбираются параметрами
`default_model`, `decision_model` и `memory.model`; отдельной конфигурации
провайдера решений нет. Новый ход сохраняет имя подключения в профиле сессии.
Ручное сжатие использует это имя; для старых сессий без него подключение
разрешается через сохранённое имя профиля в текущей конфигурации.

## Плагины

Исходники расширений находятся в `plugins/`, код их обнаружения и протокола
со стороны хоста — в `internal/plugins/`. Каталог исходников не используется
для обнаружения установленных команд: исполняемые файлы находятся в
`<home>/bin/horizon-<имя>`.

Адаптер `plugins/agent-telegram` принимает обновления, хранит привязки
чатов и передаёт вход daemon. `plugins/telegram` реализует явную отправку,
журнал состояния частей и миграцию старой установки. Общий клиент Telegram и
локальные данные этих компонентов находятся в `internal/telegram`. Ядро и
daemon используют общее понятие маршрута и разрешения коммуникации.
Daemon запускает рабочие процессы и обслуживает сессии; итоговый текст
остаётся в Horizon.

Browser находится в `plugins/browser/`, остаётся опциональным расширением
и не устанавливается по умолчанию. Memory и decision не являются плагинами.
Действующий протокол описан в [PLUGINS.md](PLUGINS.md), контракты инструментов —
в [TOOLS.md](TOOLS.md). Пересмотр протокола и MCP не входят в перенос каталогов.

## Сборка и установка

Сохраняется один корневой Go-модуль; публичный Go SDK не предоставляется.

```sh
go build -o /tmp/horizon-dev .
go test ./...
go vet ./...
make install
```

`make install-telegram` собирает `horizon-telegram`;
`make install-agent-telegram` собирает `agent-telegram`.
`make install-browser` собирает Browser. Установка выполняется из исходников.
Прежняя release-инфраструктура удалена. Новый manager собирает исходники;
публикация bootstrap-артефактов и автоматизация выпуска остаются отдельной задачей.

## Установка и обслуживание

`plugins/manager` — самостоятельный CLI `horizon-manager`; реализация установки
расположена в `internal/manager`. Он владеет bootstrap, исходниками в `source`,
бинарниками в `bin`, конфигурацией источника и состоянием `.manager`.
Основной Horizon делегирует `horizon manager` по обычному протоколу плагинов;
команды `horizon init` больше нет. Обязательный manager устанавливается с ядром,
остальные плагины выбираются пользователем. Shell-конфигурация не изменяется:
менеджер только выводит команды для ручного применения.

`internal/maintenance` согласует рабочие процессы и замену установки.
Изменение набора защищено журналом и резервными копиями; источник и state
отражают commit всего управляемого набора. Desired source находится в config.yaml,
installed commit — в `.manager/state.json`. Детали — в
[README Manager](../plugins/manager/README.md).

## Локальный daemon

`horizon-daemon` управляет очередями и внешними процессами Horizon через
Unix-сокет одного home. Адаптеры `agent-*` включаются явно. Команда
`horizon agent <имя>` передаёт административные аргументы установленному
адаптеру с capability `admin-cli`. CLI-плагины имеют префикс `horizon-*`.

Установка: `horizon manager plugins install daemon`; foreground-запуск:
`horizon daemon start`. Перед обновлением остановите его через
`horizon daemon stop`. Подробности и ограничения — [Daemon](DAEMON.md).

## Таймеры

`plugins/agent-timer` запускается как адаптер daemon и как административная
команда через `horizon agent timer`. `internal/timer` разбирает файлы, вычисляет
слоты и хранит учёт отправок. Ядро CLI передаёт аргументы адаптеру. Daemon владеет
очередью, ограничениями исполнения и созданием сессий. Каждый таймерный ход
получает новую сессию и использует переданный workspace. Контракт файла и
служебные команды описаны в [README Timer](../plugins/agent-timer/README.md).

## Subagent

Install with `horizon manager plugins install subagent`, start daemon explicitly,
and add `subagent` to `agent_plugins`. `horizon subagent status` checks connectivity.
The `subagent__spawn/list/read/send/wait/cancel` tools manage separate child sessions
in the shared workspace. Messages queue for the next turn; read access only permits
reading and waiting. An unavailable explicitly enabled daemon fails preparation.
See [Subagent](SUBAGENT.md) for schemas and unknown-outcome recovery.
