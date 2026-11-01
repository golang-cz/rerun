package main

import (
	"strings"
	"testing"
)

func TestURLCapturedFromOutputRelinksTheRow(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"url":"http://localhost:5173"}`)

	if got := m.targets[0].spec.URL; got != "http://localhost:5173" {
		t.Fatalf("spec url = %q, want the captured url", got)
	}
	leftW, _ := m.layoutWidths()
	if got := m.targetLine(0, leftW); !strings.Contains(got, "http://localhost:5173") {
		t.Fatalf("row link = %q, want the captured url", got)
	}
	m.cursor = 1
	if got := m.titleLine(80); !strings.Contains(got, "http://localhost:5173") {
		t.Fatalf("title line = %q, want the captured url", got)
	}
}

func TestURLCapturePrecedence(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"url wins over port", `{"url":"http://x","port":1}`, "http://x"},
		{"port and addr combine", `{"addr":"127.0.0.1","port":8080}`, "http://127.0.0.1:8080"},
		{"port and host combine", `{"host":"dev.local","port":3000}`, "http://dev.local:3000"},
		{"port alone is localhost", `{"port":9000}`, "http://localhost:9000"},
		{"numeric port", `{"port":9000.0}`, "http://localhost:9000"},
		{"addr with scheme stays verbatim", `{"addr":"http://x","port":1}`, "http://x"},
		{"addr verbatim", `{"addr":"http://0.0.0.0:1"}`, "http://0.0.0.0:1"},
		{"listen verbatim", `{"listen":":4317"}`, ":4317"},
		{"non-string url ignored", `{"url":[1,2]}`, ""},
		{"no keys", `{"msg":"hi"}`, ""},
		{"not json", `plain text`, ""},
		{"not an object", `[1,2,3]`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jsonLineURL(tt.line); got != tt.want {
				t.Fatalf("jsonLineURL(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}

// A line with no usable key leaves the URL the config set.
func TestURLCaptureLeavesConfiguredURLAlone(t *testing.T) {
	web := target("web")
	web.URL = "http://configured"
	m := newTestModel(t, web)
	addLines(m, "web", "plain text", `{"msg":"hi"}`, `{"url":[1]}`)

	if got := m.targets[0].spec.URL; got != "http://configured" {
		t.Fatalf("spec url = %q, want the configured url", got)
	}
}

// The last capture wins, so a restart's new port relinks the row.
func TestURLCaptureLastWins(t *testing.T) {
	m := newTestModel(t, target("web"))
	addLines(m, "web", `{"url":"http://localhost:1"}`, `{"port":2}`)

	if got := m.targets[0].spec.URL; got != "http://localhost:2" {
		t.Fatalf("spec url = %q, want the newest capture", got)
	}
}
