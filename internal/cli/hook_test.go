package cli

import (
	"strings"
	"testing"
)

func TestGenerateHookScript(t *testing.T) {
	script := generateHookScript("high", "text", 10)

	if !strings.Contains(script, hookMarkerStart) {
		t.Error("Script missing start marker")
	}
	if !strings.Contains(script, hookMarkerEnd) {
		t.Error("Script missing end marker")
	}
	if !strings.Contains(script, "prism review staged --fail-on high --format text --max-findings 10") {
		t.Error("Script missing prism command with correct flags")
	}
	if !strings.Contains(script, "PRISM_EXIT=$?") {
		t.Error("Script missing exit code capture")
	}
	if !strings.Contains(script, "exit 1") {
		t.Error("Script missing exit 1 for findings")
	}
	if !strings.Contains(script, "allowing commit") {
		t.Error("Script missing warning for errors")
	}
}

func TestGenerateHookScript_CustomFlags(t *testing.T) {
	script := generateHookScript("medium", "json", 5)

	if !strings.Contains(script, "--fail-on medium") {
		t.Error("Script doesn't use custom fail-on")
	}
	if !strings.Contains(script, "--format json") {
		t.Error("Script doesn't use custom format")
	}
	if !strings.Contains(script, "--max-findings 5") {
		t.Error("Script doesn't use custom max-findings")
	}
}

func TestReplacePrismSection_NoExisting(t *testing.T) {
	existing := "#!/bin/sh\nsome-other-hook\n"
	section := generateHookScript("high", "text", 10)

	result := replacePrismSection(existing, section)

	if !strings.HasPrefix(result, "#!/bin/sh\nsome-other-hook\n") {
		t.Error("Existing content should be preserved")
	}
	if !strings.Contains(result, hookMarkerStart) {
		t.Error("New section should be appended")
	}
	if !strings.Contains(result, "some-other-hook") {
		t.Error("Existing hook content should be preserved")
	}
}

func TestReplacePrismSection_ExistingSection(t *testing.T) {
	oldSection := generateHookScript("low", "text", 20)
	existing := "#!/bin/sh\nbefore\n" + oldSection + "after\n"
	newSection := generateHookScript("high", "json", 5)

	result := replacePrismSection(existing, newSection)

	if !strings.Contains(result, "before") {
		t.Error("Content before prism section should be preserved")
	}
	if !strings.Contains(result, "after") {
		t.Error("Content after prism section should be preserved")
	}
	if !strings.Contains(result, "--fail-on high") {
		t.Error("New section should have updated flags")
	}
	if strings.Contains(result, "--fail-on low") {
		t.Error("Old section should be replaced")
	}
}

func TestRemovePrismSection(t *testing.T) {
	section := generateHookScript("high", "text", 10)
	existing := "#!/bin/sh\nbefore\n" + section + "after\n"

	result := removePrismSection(existing)

	if strings.Contains(result, hookMarkerStart) {
		t.Error("Prism section should be removed")
	}
	if !strings.Contains(result, "before") {
		t.Error("Content before should be preserved")
	}
	if !strings.Contains(result, "after") {
		t.Error("Content after should be preserved")
	}
}

func TestRemovePrismSection_NoSection(t *testing.T) {
	existing := "#!/bin/sh\nsome-hook\n"
	result := removePrismSection(existing)
	if result != existing {
		t.Error("Content without prism section should be unchanged")
	}
}

func TestReplacePrismSection_NoTrailingNewline(t *testing.T) {
	existing := "#!/bin/sh\nsome-hook"
	section := generateHookScript("high", "text", 10)

	result := replacePrismSection(existing, section)

	if !strings.Contains(result, "some-hook") {
		t.Error("Existing content should be preserved")
	}
	if !strings.Contains(result, hookMarkerStart) {
		t.Error("Section should be appended")
	}
}
