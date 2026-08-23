package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
)

func TestGrokShellWebSearchUsesClientExecutableFunction(t *testing.T) {
	request := []byte(`{
		"model":"grok-4.6",
		"input":"Find today's news",
		"tools":[
			{"type":"function","name":"search_tool","parameters":{"type":"object"}},
			{"type":"function","name":"use_tool","parameters":{"type":"object"}},
			{"type":"web_search"}
		]
	}`)

	normalized, compatibility, err := normalizeResponsesRequest(request, "grok-4.6")
	if err != nil {
		t.Fatalf("normalizeResponsesRequest() error = %v", err)
	}
	if compatibility == nil || !compatibility.grokShellWebSearch {
		t.Fatal("Grok Shell web search compatibility was not enabled")
	}

	var payload map[string]any
	if err := json.Unmarshal(normalized, &payload); err != nil {
		t.Fatalf("decode normalized request: %v", err)
	}
	tools, _ := payload["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("normalized tools = %#v", tools)
	}
	webTool, _ := tools[2].(map[string]any)
	if webTool["type"] != "function" || webTool["name"] != "web_search" {
		t.Fatalf("web search fallback tool = %#v", webTool)
	}
	parameters, _ := webTool["parameters"].(map[string]any)
	if parameters["additionalProperties"] != false {
		t.Fatalf("web search parameters = %#v", parameters)
	}

	response := []byte(`{"output":[{"type":"function_call","name":"web_search","call_id":"call_1","arguments":"{\"query\":\"Cloudflare blog\"}"}],"tools":[{"type":"function","name":"web_search"}]}`)
	rewritten, err := compatibility.normalizeResponseJSON(response)
	if err != nil {
		t.Fatalf("normalizeResponseJSON() error = %v", err)
	}
	var downstream map[string]any
	if err := json.Unmarshal(rewritten, &downstream); err != nil {
		t.Fatalf("decode normalized response: %v", err)
	}
	output := downstream["output"].([]any)[0].(map[string]any)
	if output["type"] != "function_call" || output["name"] != "web_search" {
		t.Fatalf("downstream web search call = %#v", output)
	}
	visible := downstream["tools"].([]any)
	if len(visible) != 3 || visible[2].(map[string]any)["type"] != "web_search" {
		t.Fatalf("downstream visible tools = %#v", visible)
	}
}

func TestGrokShellWebSearchStreamRestoresFunctionCall(t *testing.T) {
	request := []byte(`{
		"tools":[
			{"type":"function","name":"search_tool","parameters":{"type":"object"}},
			{"type":"function","name":"use_tool","parameters":{"type":"object"}},
			{"type":"web_search"}
		]
	}`)
	_, compatibility, err := normalizeResponsesRequest(request, "grok-4.6")
	if err != nil {
		t.Fatalf("normalizeResponsesRequest() error = %v", err)
	}
	source := strings.Join([]string{
		`event: response.output_item.added`,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"id":"item_1","type":"function_call","call_id":"call_1","name":"web_search","arguments":""}}`,
		``,
		`event: response.function_call_arguments.delta`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"item_1","output_index":0,"delta":"{\"query\":\"Cloudflare\"}"}`,
		``,
		`event: response.output_item.done`,
		`data: {"type":"response.output_item.done","output_index":0,"item":{"id":"item_1","type":"function_call","call_id":"call_1","name":"web_search","arguments":"{\"query\":\"Cloudflare\"}"}}`,
		``,
	}, "\n")

	converted, err := io.ReadAll(compatibility.normalizeResponseStream(io.NopCloser(strings.NewReader(source))))
	if err != nil {
		t.Fatalf("read normalized stream: %v", err)
	}
	text := string(converted)
	if !strings.Contains(text, `"name":"web_search"`) || !strings.Contains(text, "Cloudflare") {
		t.Fatalf("normalized stream = %s", text)
	}
}

func TestGrokShellWebSearchFingerprintIsNarrow(t *testing.T) {
	tests := []struct {
		name  string
		tools string
	}{
		{name: "ordinary hosted search", tools: `[{"type":"web_search"}]`},
		{name: "missing use wrapper", tools: `[{"type":"function","name":"search_tool"},{"type":"web_search"}]`},
		{name: "explicit function already present", tools: `[{"type":"function","name":"search_tool"},{"type":"function","name":"use_tool"},{"type":"function","name":"web_search"},{"type":"web_search"}]`},
		{name: "hosted search has controls", tools: `[{"type":"function","name":"search_tool"},{"type":"function","name":"use_tool"},{"type":"web_search","filters":{"allowed_domains":["example.com"]}}]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var tools []any
			if err := json.Unmarshal([]byte(test.tools), &tools); err != nil {
				t.Fatalf("decode tools: %v", err)
			}
			if isGrokShellWebSearchSurface(tools) {
				t.Fatalf("unexpected Grok Shell fingerprint match: %s", test.tools)
			}
		})
	}
}

func TestGrokShellHostedWebSearchChoiceTargetsFallbackFunction(t *testing.T) {
	request := []byte(`{
		"tools":[
			{"type":"function","name":"search_tool","parameters":{"type":"object"}},
			{"type":"function","name":"use_tool","parameters":{"type":"object"}},
			{"type":"web_search"}
		],
		"tool_choice":{"type":"web_search"}
	}`)
	normalized, _, err := normalizeResponsesRequest(request, "grok-4.6")
	if err != nil {
		t.Fatalf("normalizeResponsesRequest() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(normalized, &payload); err != nil {
		t.Fatalf("decode normalized request: %v", err)
	}
	choice, _ := payload["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["name"] != "web_search" {
		t.Fatalf("normalized tool_choice = %#v", choice)
	}
}
