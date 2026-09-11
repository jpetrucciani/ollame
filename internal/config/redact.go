package config

import (
	"net/url"
	"reflect"
	"regexp"
	"strings"
)

// Redacted exports TOML-shaped effective configuration using type annotations,
// including nested token arrays and every value in secret header maps.
func (cfg Config) Redacted() map[string]any { return redactStruct(reflect.ValueOf(cfg)) }

func redactStruct(value reflect.Value) map[string]any {
	out := map[string]any{}
	for i := 0; i < value.NumField(); i++ {
		field := value.Type().Field(i)
		v := value.Field(i)
		if field.Anonymous {
			for key, value := range redactStruct(v) {
				out[key] = value
			}
			continue
		}
		key := strings.Split(field.Tag.Get("toml"), ",")[0]
		if key == "" || key == "-" {
			continue
		}
		if field.Tag.Get("secret") == "true" {
			if v.Kind() == reflect.Map {
				headers := map[string]string{}
				for _, name := range v.MapKeys() {
					headers[name.String()] = "<redacted>"
				}
				out[key] = headers
			} else {
				out[key] = "<redacted>"
			}
			continue
		}
		out[key] = redactValue(v)
		if key == "base_url" {
			if text, ok := out[key].(string); ok {
				out[key] = StripURLUserinfo(text)
			}
		}
	}
	return out
}
func redactValue(value reflect.Value) any {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return redactValue(value.Elem())
	}
	switch value.Kind() {
	case reflect.Struct:
		return redactStruct(value)
	case reflect.Slice:
		items := make([]any, value.Len())
		for i := range items {
			items[i] = redactValue(value.Index(i))
		}
		return items
	default:
		return value.Interface()
	}
}

func StripURLUserinfo(text string) string {
	parsed, err := url.Parse(text)
	if err != nil {
		return "<invalid URL>"
	}
	parsed.User = nil
	return parsed.String()
}

type Redactor struct {
	tokens   *regexp.Regexp
	userInfo *regexp.Regexp
}

func NewRedactor() (Redactor, error) {
	tokens, err := regexp.Compile(`olm_[A-Za-z0-9]{43}|sk-[A-Za-z0-9_-]{20,}`)
	if err != nil {
		return Redactor{}, err
	}
	userInfo, err := regexp.Compile(`(https?://)[^\s/@]+@`)
	if err != nil {
		return Redactor{}, err
	}
	return Redactor{tokens: tokens, userInfo: userInfo}, nil
}
func (r Redactor) UpstreamError(body string) string {
	if r.tokens == nil || r.userInfo == nil {
		return "<redacted upstream error>"
	}
	clean := r.tokens.ReplaceAllString(body, "<redacted>")
	clean = r.userInfo.ReplaceAllString(clean, "${1}")
	if len(clean) > 2048 {
		clean = clean[:2048]
	}
	return strings.ToValidUTF8(clean, "")
}
