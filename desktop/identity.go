package main

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
)

func singleInstanceID(home, platform string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(home)))
	profile := fmt.Sprintf("%x", digest[:12])
	// D-Bus names cannot have a component beginning with a digit. Keep IDs
	// unchanged for installations that could already start, including other OSes.
	if platform == "linux" && profile[0] >= '0' && profile[0] <= '9' {
		profile = "p" + profile
	}
	return "ne.saturday.q-studio." + profile
}
