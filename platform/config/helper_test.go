package config_test

import (
	"os"
	"reflect"
)

// unset removes a variable outright. t.Setenv only sets, and an empty string is
// not the same as absent for a `required` tag, so the defaults tests need both.
// t.Setenv has already recorded the original value for restoration.
func unset(key string) error { return os.Unsetenv(key) }

// field is one struct field and its env tag.
type field struct {
	name string
	tag  string
}

// reflectFields lists a config struct's fields and their env tags.
func reflectFields(v any) []field {
	t := reflect.TypeOf(v)
	out := make([]field, 0, t.NumField())
	for i := range t.NumField() {
		f := t.Field(i)
		out = append(out, field{name: f.Name, tag: f.Tag.Get("env")})
	}
	return out
}
