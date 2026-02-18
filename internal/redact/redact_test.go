package redact

import (
	"strings"
	"testing"
)

func TestSecrets_APIKeys(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"AWS access key", "AKIAIOSFODNN7EXAMPLE"},
		{"Bearer token", "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"},
		{"Generic API key assignment", `api_key = "sk-1234567890abcdefghijklmn"`},
		{"JWT", "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"},
		{"Private key", "-----BEGIN PRIVATE KEY-----"},
		{"GitHub token", "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"},
		{"Slack token", "xoxb-123456789-abcdefghij"},
		{"Anthropic key", "sk-ant-abcdefghijklmnopqrstuvwxyz"},
		{"OpenAI key", "sk-abcdefghijklmnopqrstuvwxyz"},
		{"Secret assignment", `password = "my-super-secret-password-123"`},
		{"Token assignment", `token: "abcdef1234567890abcdef1234567890"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := Secrets(tt.input)
			if strings.Contains(result, tt.input) && result != placeholder {
				// The original secret text should not survive redaction
				// (unless the whole thing became [REDACTED])
				if result != placeholder {
					// Check it was at least partially redacted
					if !strings.Contains(result, placeholder) {
						t.Errorf("Expected redaction for %s, got: %s", tt.name, result)
					}
				}
			}
		})
	}
}

func TestSecrets_NoFalsePositives(t *testing.T) {
	inputs := []string{
		"just some normal code",
		"func main() { fmt.Println(\"hello\") }",
		"x := 42",
		"// this is a comment about API design",
	}
	for _, input := range inputs {
		result := Secrets(input)
		if result != input {
			t.Errorf("False positive redaction:\n  input:  %s\n  output: %s", input, result)
		}
	}
}

func TestShouldRedactPath(t *testing.T) {
	patterns := []string{"**/.env", "**/*secrets*"}

	tests := []struct {
		path string
		want bool
	}{
		{".env", true},
		{"config/.env", true},
		{"secrets.yaml", true},
		{"my-secrets-file.json", true},
		{"main.go", false},
		{"config/app.json", false},
	}

	for _, tt := range tests {
		got := ShouldRedactPath(tt.path, patterns)
		if got != tt.want {
			t.Errorf("ShouldRedactPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestContent_PathRedaction(t *testing.T) {
	result := Content("some content", ".env", []string{"**/.env"})
	if !strings.Contains(result, placeholder) {
		t.Error("Expected path-based redaction for .env file")
	}
	if strings.Contains(result, "some content") {
		t.Error("Content should be fully redacted for .env file")
	}
}

func TestContent_SecretRedaction(t *testing.T) {
	input := `API_KEY = "sk-ant-abcdefghijklmnopqrstuvwxyz"`
	result := Content(input, "main.go", []string{"**/.env"})
	if strings.Contains(result, "sk-ant-") {
		t.Error("Expected secret to be redacted in content")
	}
}
