package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ReanSn0w/horizon/internal/eventstream"
)

func newEventPublisher(mode string, verbose bool, out, errOut io.Writer) eventstream.Publish {
	if mode == "plain" {
		return func(event eventstream.Event) {
			if event.Type == "plugin_finalize_failed" {
				data, _ := event.Data.(map[string]any)
				fmt.Fprintf(errOut, "Horizon: завершение плагина %v: %v\n", value(data, "plugin"), value(data, "message"))
			}
		}
	}
	if mode == "jsonl" {
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(false)
		return func(event eventstream.Event) { _ = encoder.Encode(event) }
	}
	return func(event eventstream.Event) {
		data, _ := event.Data.(map[string]any)
		switch event.Type {
		case "plugin_finalize_failed":
			fmt.Fprintf(errOut, "Horizon: завершение плагина %v: %v\n", value(data, "plugin"), value(data, "message"))
		case "turn_started":
			fmt.Fprintf(errOut, "Horizon: ход %v, модель %v\n", value(data, "model_profile"), value(data, "model"))
		case "tool_started":
			fmt.Fprintf(errOut, "\n→ %v%s\n", data["name"], toolTarget(data["arguments"]))
		case "tool_completed":
			result, ok, shellExit, truncated, paths, processStatus := describeToolResult(data["result"])
			status := "ошибка"
			if ok {
				status = "готово"
			}
			if processStatus == "running" {
				status = "работает"
			}
			extra := ""
			if shellExit != "" {
				extra += ", exit " + shellExit
			}
			if truncated {
				extra += ", результат сокращён"
			}
			fmt.Fprintf(errOut, "✓ %v: %s%s (%v ms)\n", data["name"], status, extra, data["duration_ms"])
			if verbose {
				fmt.Fprintf(errOut, "  result: %s\n", result)
				for _, path := range paths {
					fmt.Fprintf(errOut, "  full log: %s\n", path)
				}
			}
		case "compaction_started":
			fmt.Fprintln(errOut, "Сжатие контекста…")
		case "processes_completed":
			fmt.Fprintln(errOut, "Управляемые команды завершены.")
		case "compaction_completed":
			fmt.Fprintln(errOut, "Контекст сжат.")
		case "turn_completed":
			if usage, ok := data["usage"]; ok && verbose {
				encoded, _ := json.Marshal(usage)
				fmt.Fprintf(errOut, "\nusage: %s\n", encoded)
			}
		}
	}
}

func value(data map[string]any, key string) any {
	if value, ok := data[key]; ok {
		return value
	}
	return "-"
}

func toolTarget(raw any) string {
	data, ok := raw.(json.RawMessage)
	if !ok {
		return ""
	}
	var arguments map[string]any
	if json.Unmarshal(data, &arguments) != nil {
		return ""
	}
	for _, key := range []string{"path", "command", "name"} {
		if target, ok := arguments[key].(string); ok && target != "" {
			return ": " + target
		}
	}
	return ""
}

func describeToolResult(raw any) (encoded string, ok bool, shellExit string, truncated bool, paths []string, processStatus string) {
	data, valid := raw.(json.RawMessage)
	if !valid {
		return "null", false, "", false, nil, ""
	}
	encoded = strings.TrimSpace(string(data))
	var result struct {
		OK   bool           `json:"ok"`
		Data map[string]any `json:"data"`
	}
	if json.Unmarshal(data, &result) != nil {
		return encoded, false, "", false, nil, ""
	}
	processStatus, _ = result.Data["status"].(string)
	if exit, exists := result.Data["exit_code"]; exists {
		if exit == nil {
			shellExit = "signal"
		} else {
			shellExit = fmt.Sprint(exit)
		}
	}
	truncated, _ = result.Data["truncated"].(bool)
	for _, key := range []string{"stdout_path", "stderr_path"} {
		if path, ok := result.Data[key].(string); ok && path != "" {
			paths = append(paths, path)
		}
	}
	return encoded, result.OK, shellExit, truncated, paths, processStatus
}
