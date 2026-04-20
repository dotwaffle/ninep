package client

import (
	"reflect"
	"testing"
)

// TestSplitPath covers the behaviours the session-method callers depend
// on: root-like paths collapse to nil (the 0-name Twalk shape), multi-
// component paths split cleanly, leading/trailing/duplicate slashes are
// ignored, and ".."/"." are normalized lexically by path.Clean before
// the split so the walked component list never carries them over the
// wire.
func TestSplitPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"root_slash", "/", nil},
		{"empty", "", nil},
		{"dot", ".", nil},
		{"single", "/a", []string{"a"}},
		{"double", "/a/b/c", []string{"a", "b", "c"}},
		{"no_leading_slash", "a/b", []string{"a", "b"}},
		{"duplicate_slashes", "//a//b/", []string{"a", "b"}},
		{"lex_dotdot", "/a/../b", []string{"b"}},
		{"trailing_dot", "/a/./b", []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := splitPath(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("splitPath(%q) = %#v, want %#v", tc.in, got, tc.want)
			}
		})
	}
}
