package clipboard

import (
	"reflect"
	"testing"
)

func TestExtractURLs(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"no links here", []string{}},
		{"https://example.com/file.iso", []string{"https://example.com/file.iso"}},
		{"see (https://example.com/a.zip).", []string{"https://example.com/a.zip"}},
		{"<https://example.com/x?q=1&b=2>", []string{"https://example.com/x?q=1&b=2"}},
		{"ftp://example.com/file", []string{}},
		{"a http://one.test/1 and https://two.test/2!", []string{"http://one.test/1", "https://two.test/2"}},
	}
	for _, c := range cases {
		got := ExtractURLs(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("ExtractURLs(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}
