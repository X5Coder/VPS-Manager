package dockerx

import "testing"

func TestParseDockerSize(t *testing.T) {
	if ParseDockerSize("20.94GB") < 20e9 {
		t.Fatal("gb")
	}
	if ParseDockerSize("720.8MB (3%)") < 700e6 {
		t.Fatal("mb")
	}
	if ParseDockerSize("0B") != 0 {
		t.Fatal("zero")
	}
}
