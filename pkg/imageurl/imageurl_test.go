package imageurl

import (
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	long := "https://cdn.example.com/" + strings.Repeat("a", MaxLen)
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"", true},
		{"https://cdn.example.com/a.jpg", true},
		{"https://picsum.photos/seed/blt/640/480", true},
		{"http://cdn.example.com/a.jpg", false},
		{"javascript:alert(1)", false},
		{"data:image/png;base64,AAAA", false},
		{"//cdn.example.com/a.jpg", false},
		{"https://", false},
		{"https:///a.jpg", false},
		{"https://user:pw@cdn.example.com/a.jpg", false},
		{"https://cdn.example.com/a b.jpg", false},
		{"https://cdn.example.com/a\n.jpg", false},
		{long, false},
	} {
		if err := Validate(tc.in); (err == nil) != tc.ok {
			t.Errorf("Validate(%.40q) = %v, want ok=%v", tc.in, err, tc.ok)
		}
	}
}
