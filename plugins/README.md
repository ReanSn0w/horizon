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
