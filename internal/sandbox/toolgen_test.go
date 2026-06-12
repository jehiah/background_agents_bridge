package sandbox

import (
	"strings"
	"testing"
)

// TestToolDefsMatchImpls ensures every generated tool definition has an executor
// and vice versa — the JS shim and the CLI dispatch must stay in lockstep.
func TestToolDefsMatchImpls(t *testing.T) {
	defs := strings.Join(toolDefNames(), ",")
	impls := strings.Join(ToolNames(), ",")
	if defs != impls {
		t.Fatalf("toolDefs (%s) != toolImpls (%s)", defs, impls)
	}
}

func TestGenerateToolJS(t *testing.T) {
	var def toolDef
	for _, d := range toolDefs {
		if d.name == "create-pull-request" {
			def = d
		}
	}
	js := generateToolJS(def, "/usr/local/bin/bridge")

	for _, want := range []string{
		`const BRIDGE_BIN = "/usr/local/bin/bridge";`,
		`name: "create-pull-request",`,
		`spawn(BRIDGE_BIN, ["tool", name]`,
		`return await runBridgeTool("create-pull-request", args);`,
		`import { tool } from "@opencode-ai/plugin";`,
		`baseBranch: z`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("generated JS missing %q\n---\n%s", want, js)
		}
	}
}

func TestGenerateToolJSEscapesDescription(t *testing.T) {
	// Descriptions contain quotes/backticks; they must be JSON-encoded so the JS
	// stays valid.
	for _, d := range toolDefs {
		js := generateToolJS(d, "/bin/bridge")
		if !strings.Contains(js, "name: "+jsString(d.name)+",") {
			t.Errorf("%s: name not JSON-encoded", d.name)
		}
		if !strings.Contains(js, "description: "+jsString(d.description)+",") {
			t.Errorf("%s: description not JSON-encoded", d.name)
		}
	}
}
