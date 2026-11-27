//go:build windows

package providerenv

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestParseEnvironmentBlock(t *testing.T) {
	var block []uint16
	for _, entry := range []string{"=C:=C:\\q", "Q_TEST_KEY=rotated", "Path=C:\\Windows"} {
		encoded, err := windows.UTF16FromString(entry)
		if err != nil {
			t.Fatal(err)
		}
		block = append(block, encoded...)
	}
	block = append(block, 0)
	values := parseEnvironmentBlock(&block[0])
	if len(values) != 2 || values["Q_TEST_KEY"] != "rotated" || values["Path"] != `C:\Windows` {
		t.Fatalf("values = %v", values)
	}
}

func TestUserEnvironmentReadsCurrentUser(t *testing.T) {
	values, err := userEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := values["SystemRoot"]; !ok {
		t.Fatalf("SystemRoot missing from %d variables", len(values))
	}
}
