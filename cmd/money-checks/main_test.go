package main

import (
	"bytes"
	"testing"
)

func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != exitOK {
		t.Fatalf("exit code %d, stderr %q", code, errOut.String())
	}
	if got := out.String(); got != "dev\n" {
		t.Fatalf("version printed %q", got)
	}
}

func TestUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"nope"}, &out, &errOut); code != exitError {
		t.Fatalf("exit code %d", code)
	}
	if errOut.Len() == 0 {
		t.Fatal("no usage on stderr")
	}
}
