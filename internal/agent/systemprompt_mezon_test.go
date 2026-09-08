package agent

import (
	"strings"
	"testing"
)

func TestToolingSectionExplainsMezonInteractiveInsteadOfCallingItCustom(t *testing.T) {
	section := strings.Join(buildToolingSection([]string{"mezon_interactive"}, false, nil), "\n")
	if strings.Contains(section, "(custom tool)") {
		t.Fatalf("Mezon rich messaging is undiscoverable: %q", section)
	}
	if !strings.Contains(section, "buttons") || !strings.Contains(section, "Mezon") {
		t.Fatalf("Mezon rich messaging description is not actionable: %q", section)
	}
}
