package hub

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"
)

var fields = map[string]string{
	"search": "query types scope tags since limit offset",
	"read":   "type id revision history attachment_id limit offset",
	"save":   "type id expected_revision body scope conditions tags sources reason provenance source_time metadata attachments",
}
var required = map[string]string{"read": "type id", "save": "type body"}

func Schema(op string) map[string]any {
	all := schemaType(reflect.TypeOf(Params{}))["properties"].(map[string]any)
	props := map[string]any{}
	req := []string{}
	for _, name := range strings.Fields(fields[op]) {
		props[name] = all[name]
	}
	req = append(req, strings.Fields(required[op])...)
	if IsWrite(op) {
		props["idempotency_key"] = all["idempotency_key"]
		req = append(req, "idempotency_key")
	}
	if property, ok := props["type"].(map[string]any); ok {
		property["enum"] = []string{"source", "knowledge"}
	}
	if property, ok := props["idempotency_key"].(map[string]any); ok {
		property["minLength"] = 1
		property["maxLength"] = 200
	}
	return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
}
func schemaType(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Pointer {
		return schemaType(t.Elem())
	}
	if t == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Int, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": schemaType(t.Elem())}
	case reflect.Struct:
		props := map[string]any{}
		req := []string{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")
			if tag[0] == "" || tag[0] == "-" {
				continue
			}
			props[tag[0]] = schemaType(f.Type)
			if len(tag) == 1 {
				req = append(req, tag[0])
			}
		}
		return map[string]any{"type": "object", "properties": props, "required": req, "additionalProperties": false}
	default:
		return map[string]any{"type": "object", "additionalProperties": true}
	}
}
func DecodeOperation(op string, raw []byte) (Params, error) {
	if !KnownOperation(op) {
		return Params{}, fail("unknown_operation", "unknown operation")
	}
	p, err := Decode(raw)
	if err != nil {
		return p, err
	}
	var obj map[string]json.RawMessage
	if err = json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return p, fail("invalid_input", "JSON object required")
	}
	schema := Schema(op)
	props := schema["properties"].(map[string]any)
	for key, value := range obj {
		if _, ok := props[key]; !ok {
			return p, fail("invalid_input", "unexpected field: "+key)
		}
		if string(value) == "null" {
			return p, fail("invalid_input", "omit optional fields instead of null: "+key)
		}
	}
	for _, key := range schema["required"].([]string) {
		if _, ok := obj[key]; !ok {
			return p, fail("invalid_input", "required field: "+key)
		}
	}
	return p, nil
}
