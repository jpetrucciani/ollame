package glob

import "testing"

func TestFullIDMatching(t *testing.T) {
	for _, tc := range []struct {
		pattern, input string
		want           bool
	}{
		{"*", "openai/gpt-oss-120b", true}, {"openai/*", "openai/a/b", true}, {"*", "a\nb", true},
		{"?", "é", true}, {"?", "ab", false}, {"[a-c]?", "b/", true}, {"[!a-c]*", "z/abc", true},
		{"[!a-c]*", "b/abc", false}, {"openai/*", "OpenAI/a", false}, {`a\*b`, "a*b", true},
		{"a", "aa", false}, {"a", "a", true}, {"", "", true}, {"", "x", false},
	} {
		t.Run(tc.pattern+"/"+tc.input, func(t *testing.T) {
			p, err := Compile(tc.pattern)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Match(tc.input); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
func TestMalformedPatterns(t *testing.T) {
	for _, p := range []string{"[", "[]", "[!]", `abc\`, "[z-a]"} {
		if _, err := Compile(p); err == nil {
			t.Errorf("accepted %q", p)
		}
	}
}
func FuzzStarMatchesNamespaces(f *testing.F) {
	for _, s := range []string{"openai/gpt-oss-120b", "a/b/c", "é/模型", "", "x\ny"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		p, err := Compile("*")
		if err != nil {
			t.Fatal(err)
		}
		if !p.Match(s) {
			t.Fatalf("wildcard excluded input %q", s)
		}
	})
}
