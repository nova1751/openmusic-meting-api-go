package providerhttp

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Object handles upstream responses with alternate field names and string IDs.
type Object map[string]any

func AsObject(value any) Object {
	if object, ok := value.(Object); ok {
		return object
	}
	item, _ := value.(map[string]any)
	return item
}

func (o Object) Get(fields ...string) any {
	for _, field := range fields {
		if v := o[field]; v != nil {
			if text, isString := v.(string); !isString || strings.TrimSpace(text) != "" {
				return v
			}
		}
	}
	return nil
}

func (o Object) Object(fields ...string) Object {
	for _, field := range fields {
		if item := AsObject(o[field]); item != nil {
			return item
		}
	}
	return nil
}

func (o Object) List(fields ...string) []any {
	for _, field := range fields {
		if item, ok := o[field].([]any); ok {
			return item
		}
	}
	return nil
}

func Text(value any) string {
	switch value := value.(type) {
	case string:
		return strings.TrimSpace(value)
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

func (o Object) Text(fields ...string) string {
	for _, field := range fields {
		if value := Text(o[field]); value != "" {
			return value
		}
	}
	return ""
}

func Number(value any) float64 {
	number, _ := strconv.ParseFloat(Text(value), 64)
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0
	}
	return number
}

func (o Object) Int(fields ...string) int64 {
	value, err := strconv.ParseInt(o.Text(fields...), 10, 64)
	if err == nil {
		return value
	}
	return 0
}

func First(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func RawString(s string) json.RawMessage {
	data, _ := json.Marshal(s)
	return data
}

func Positive(value any) bool {
	flag, _ := value.(bool)
	return flag || Number(value) > 0 || strings.EqualFold(Text(value), "true")
}

func Decode(data []byte) (Object, error) {
	var object Object
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	err := decoder.Decode(&object)
	return object, err
}
