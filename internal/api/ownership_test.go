package api

import (
	"reflect"
	"testing"
)

func TestSanitizeTags(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
		want map[string]string
	}{
		{"nil", nil, nil},
		{"trims", map[string]string{" team ": " web "}, map[string]string{"team": "web"}},
		{"drops empty key", map[string]string{"": "x", "env": "prod"}, map[string]string{"env": "prod"}},
		{"all empty keys → nil", map[string]string{"  ": "x"}, nil},
		{"empty value kept", map[string]string{"flag": ""}, map[string]string{"flag": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeTags(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("sanitizeTags(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
