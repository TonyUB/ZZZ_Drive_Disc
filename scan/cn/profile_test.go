package cn

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadINI(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.ini")
	if err := os.WriteFile(path, []byte("[General]\ngame_version=3.0.0\nchannel = 1\nsub_channel=2\n; comment\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := readINI(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["game_version"] != "3.0.0" || values["channel"] != "1" || values["sub_channel"] != "2" {
		t.Fatalf("unexpected values: %#v", values)
	}
}
