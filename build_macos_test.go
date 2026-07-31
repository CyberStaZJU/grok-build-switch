package main

import (
	"os"
	"strings"
	"testing"
)

func TestMacOSBuildUsesAppleCompatibleBundleVersions(t *testing.T) {
	data, err := os.ReadFile("build-macos.sh")
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, fragment := range []string{
		`MARKETING_VERSION="${MARKETING_VERSION:-${VERSION:-0.0.0}}"`,
		`BUILD_VERSION="${BUILD_VERSION:-${BUILD_NUMBER:-1}}"`,
		`^v[0-9]`,
		`^[0-9]+\.[0-9]+\.[0-9]+$`,
		`^[0-9]+(\.[0-9]+){0,2}$`,
		`<key>CFBundleShortVersionString</key><string>${MARKETING_VERSION}</string>`,
		`<key>CFBundleVersion</key><string>${BUILD_VERSION}</string>`,
	} {
		if !strings.Contains(script, fragment) {
			t.Fatalf("build script is missing bundle version contract %q", fragment)
		}
	}
	if strings.Contains(script, `${GITHUB_REF_NAME:-`) {
		t.Fatal("branch or tag names still feed bundle version fields")
	}
	if strings.Contains(script, `<key>CFBundleVersion</key><string>${VERSION}</string>`) {
		t.Fatal("marketing VERSION is still reused as CFBundleVersion")
	}
}
