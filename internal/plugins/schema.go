package plugins

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
)

// CheckSchema deliberately implements a small strict subset instead of accepting
// schema keywords that the host cannot enforce.
func CheckSchema(schema map[string]any) error {
	if err := checkSchema(schema, 0); err != nil {
		return err
	}
	if schema["type"] != "object" {
		return fmt.Errorf("tool parameters must be an object schema")
	}
	return nil
}
func types(schema map[string]any) ([]string, error) {
	switch v := schema["type"].(type) {
	case string:
		return []string{v}, nil
	case []any:
		r := make([]string, len(v))
		for i, t := range v {
			s, ok := t.(string)
			if !ok {
				return nil, fmt.Errorf("invalid schema type")
			}
			r[i] = s
		}
		return r, nil
	case []string:
		return v, nil
	}
	return nil, fmt.Errorf("schema type is required")
}
func checkSchema(s map[string]any, depth int) error {
	if depth > 16 {
		return fmt.Errorf("schema nesting exceeds 16")
	}
	for k := range s {
		switch k {
		case "type", "description", "enum", "properties", "required", "additionalProperties", "items":
		default:
			return fmt.Errorf("unsupported schema keyword %q", k)
		}
	}
	ts, err := types(s)
	if err != nil || len(ts) == 0 {
		return fmt.Errorf("invalid schema type")
	}
	seen := map[string]bool{}
	hasObj, hasArray := false, false
	for _, t := range ts {
		if seen[t] {
			return fmt.Errorf("duplicate schema type")
		}
		seen[t] = true
		switch t {
		case "object":
			hasObj = true
		case "array":
			hasArray = true
		case "string", "number", "integer", "boolean", "null":
		default:
			return fmt.Errorf("unsupported schema type %q", t)
		}
	}
	if d, ok := s["description"]; ok {
		if _, ok := d.(string); !ok {
			return fmt.Errorf("description must be a string")
		}
	}
	if hasObj {
		props, ok := s["properties"].(map[string]any)
		if !ok {
			return fmt.Errorf("object properties required")
		}
		if s["additionalProperties"] != false {
			return fmt.Errorf("additionalProperties must be false")
		}
		raw, ok := s["required"]
		if !ok {
			return fmt.Errorf("required fields missing")
		}
		data, _ := json.Marshal(raw)
		var req []string
		if json.Unmarshal(data, &req) != nil || len(req) != len(props) {
			return fmt.Errorf("all properties must be required")
		}
		seen := map[string]bool{}
		for _, p := range req {
			if _, ok := props[p]; !ok || seen[p] {
				return fmt.Errorf("invalid required field %q", p)
			}
			seen[p] = true
		}
		for _, p := range props {
			child, ok := p.(map[string]any)
			if !ok {
				return fmt.Errorf("invalid property schema")
			}
			if err := checkSchema(child, depth+1); err != nil {
				return err
			}
		}
	} else {
		for _, k := range []string{"properties", "required", "additionalProperties"} {
			if _, ok := s[k]; ok {
				return fmt.Errorf("%s requires object type", k)
			}
		}
	}
	if hasArray {
		child, ok := s["items"].(map[string]any)
		if !ok {
			return fmt.Errorf("array items schema required")
		}
		if err := checkSchema(child, depth+1); err != nil {
			return err
		}
	} else if _, ok := s["items"]; ok {
		return fmt.Errorf("items requires array type")
	}
	if e, ok := s["enum"]; ok {
		values, ok := e.([]any)
		if !ok || len(values) == 0 {
			return fmt.Errorf("enum must be a nonempty array")
		}
		for _, v := range values {
			if err := validateValue(s, v, false); err != nil {
				return fmt.Errorf("enum value: %w", err)
			}
		}
	}
	return nil
}
func Validate(schema map[string]any, arguments json.RawMessage) error {
	var value any
	if err := Decode(arguments, &value); err != nil {
		return err
	}
	return validateValue(schema, value, true)
}
func validateValue(s map[string]any, v any, enum bool) error {
	ts, _ := types(s)
	matched := false
	for _, t := range ts {
		switch t {
		case "null":
			matched = matched || v == nil
		case "string":
			_, ok := v.(string)
			matched = matched || ok
		case "boolean":
			_, ok := v.(bool)
			matched = matched || ok
		case "number":
			_, ok := v.(json.Number)
			matched = matched || ok
		case "integer":
			if n, ok := v.(json.Number); ok {
				r, ok := new(big.Rat).SetString(string(n))
				matched = matched || ok && r.IsInt()
			}
		case "object":
			_, ok := v.(map[string]any)
			matched = matched || ok
		case "array":
			_, ok := v.([]any)
			matched = matched || ok
		}
	}
	if !matched {
		return fmt.Errorf("value does not match schema type")
	}
	if enum {
		if values, ok := s["enum"].([]any); ok {
			found := false
			for _, x := range values {
				found = found || reflect.DeepEqual(v, x)
			}
			if !found {
				return fmt.Errorf("value is outside enum")
			}
		}
	}
	if obj, ok := v.(map[string]any); ok {
		props := s["properties"].(map[string]any)
		if len(obj) != len(props) {
			return fmt.Errorf("object must contain exactly the required properties")
		}
		for k, p := range props {
			val, ok := obj[k]
			if !ok {
				return fmt.Errorf("%s is required", k)
			}
			if err := validateValue(p.(map[string]any), val, true); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	}
	if arr, ok := v.([]any); ok {
		for _, x := range arr {
			if err := validateValue(s["items"].(map[string]any), x, true); err != nil {
				return err
			}
		}
	}
	return nil
}
