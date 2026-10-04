# Browser Use plugin

`make install-browser HORIZON_HOME=/path/to/home` installs the plugin as
`<home>/plugins/horizon-browser`. It is not part of the default plugin set.

Add to `<home>/config.yaml`:

```yaml
agent_plugins: [browser]
plugins:
  browser:
    api_key: YOUR_BROWSER_USE_API_KEY
    timeout_minutes: 10
```

Use `horizon resume --access full` for browser tools. The token stays in the
Horizon config. `api_url` defaults to `https://api.browser-use.com/api/v4` and
`action_timeout` defaults to `30s`. The plugin validates its own section
strictly. The metadata and help commands work without a configured home.

The browser is created when the model first navigates to a URL in a turn and
stopped when that turn ends. After a crash, use `horizon browser list` and
`horizon browser close <id>` or `horizon browser close --stale` to inspect and
stop remaining sessions. Active remote browsers may incur provider charges.
`SIGKILL` and power loss can prevent immediate cleanup. If the API is
unavailable, the plugin cannot confirm whether a remote session is still
active; retry inspection once the API is reachable.
