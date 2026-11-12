package preflight

import (
	"os"
	"strings"
	"testing"
)

func TestEnvExampleIsSafeAndDocumented(t *testing.T) {
	b, err := os.ReadFile("../../.env.example")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, Prefix) {
			env = append(env, line)
			key, _, _ := strings.Cut(line, "=")
			if !strings.Contains(string(doc), "`"+strings.TrimPrefix(key, Prefix)+"`") {
				t.Errorf("undocumented key %s", key)
			}
		}
	}
	c, err := Parse(env)
	if err != nil {
		t.Fatal(err)
	}
	if !c.DryRun {
		t.Fatal("unsafe example")
	}
	for _, ch := range c.Checks {
		if ch.Enabled {
			t.Fatal("example enables a check")
		}
	}
}
