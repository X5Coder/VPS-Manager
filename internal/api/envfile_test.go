package api

import "testing"

func TestParseEnvMap(t *testing.T) {
	rows := parseEnvMap("A=1\n# c\nB=two\n")
	if len(rows) != 2 || rows[0][0] != "A" || rows[1][1] != "two" {
		t.Fatalf("%v", rows)
	}
	m := envMapFromRows(rows)
	if m["A"] != "1" {
		t.Fatal(m)
	}
	out := renderEnvRows(rows)
	if out != "A=1\nB=two\n" {
		t.Fatalf("%q", out)
	}
}
