package gateway

import (
	"bytes"
	"testing"
)

// Verifies the full observe->classify path against the real stub shape: a
// Responses-API stream that carries only a function_call item and usage
// (output_tokens=24, reasoning_tokens=0) with zero text deltas. This is the
// upstream no-reasoning stub that previously rode the short-output
// exemption to a silent delivery.
func TestStubStreamWithholdsEndToEnd(t *testing.T) {
	state := &qualityScanState{protocol: qualityProtocolResponses}
	fixtures := []string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n",
		`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc_1","name":"grep","arguments":"{}"}}` + "\n",
		`data: {"type":"response.function_call_arguments.delta","delta":"{\"pattern\":\"web\"}"}` + "\n",
		`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","name":"grep","arguments":"{\"pattern\":\"web\"}"}}` + "\n",
		`data: {"type":"response.completed","response":{"id":"resp_1","output":[{"type":"function_call","id":"fc_1","name":"grep","arguments":"{\"pattern\":\"web\"}"}],"usage":{"input_tokens":250000,"output_tokens":24,"total_tokens":250024,"output_tokens_details":{"reasoning_tokens":0}}}}` + "\n",
		"data: [DONE]\n",
	}
	for _, f := range fixtures {
		ObserveQualityChunk(state, []byte(f))
	}
	sig := state.signals()
	if sig.HasThinking || sig.HasVisibleText {
		t.Fatalf("stub stream misread: HasThinking=%v HasVisibleText=%v", sig.HasThinking, sig.HasVisibleText)
	}
	if got := ClassifyQualityHold(sig, 32); got != QualityWithhold {
		t.Fatalf("stub stream verdict = %s, want withhold (signals: %+v)", got, sig)
	}

	// Control: the same stream plus one text delta must still deliver.
	state2 := &qualityScanState{protocol: qualityProtocolResponses}
	fixtures[1] = `data: {"type":"response.output_text.delta","delta":"OK"}` + "\n"
	for _, f := range fixtures {
		ObserveQualityChunk(state2, []byte(f))
	}
	sig2 := state2.signals()
	if !sig2.HasVisibleText {
		t.Fatalf("text-bearing stream lost HasVisibleText")
	}
	if got := ClassifyQualityHold(sig2, 32); got != QualityDeliver {
		t.Fatalf("short text verdict = %s, want deliver (signals: %+v)", got, sig2)
	}
}

func TestReasoningExpectedExtraction(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`{"model":"grok-4.6","reasoning_effort":"xhigh"}`, true},
		{`{"model":"grok-4.6","reasoning_effort":"high"}`, true},
		{`{"model":"grok-4.6","reasoning":{"effort":"max"}}`, true},
		{`{"model":"grok-4.6","reasoning_effort":"low"}`, false},
		{`{"model":"grok-4.6","reasoning_effort":"none"}`, false},
		{`{"model":"grok-4.6"}`, false},
		{`{"model":"grok-4.6","reasoning":{"effort":"medium"}}`, false},
	}
	for _, c := range cases {
		if got := qualityRequestExpectsReasoning([]byte(c.body)); got != c.want {
			t.Fatalf("qualityRequestExpectsReasoning(%s) = %v, want %v", c.body, got, c.want)
		}
	}
}

// The observed degradation shape: an xhigh-effort agentic request receives a
// short coherent intent statement ("继续打 OA 和心理系统。") with zero
// thinking. The stream carries visible text, so the earlier HasVisibleText
// guard does not apply; only effort-awareness withholds it.
func TestEffortfulShortIntentStatementWithholds(t *testing.T) {
	state := &qualityScanState{protocol: qualityProtocolResponses, reasoningExpected: true}
	fixtures := []string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n",
		`data: {"type":"response.output_text.delta","delta":"继续打 OA 和心理系统。"}` + "\n",
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":152822,"output_tokens":17,"total_tokens":152839,"output_tokens_details":{"reasoning_tokens":0}}}}` + "\n",
		"data: [DONE]\n",
	}
	for _, f := range fixtures {
		ObserveQualityChunk(state, []byte(f))
	}
	sig := state.signals()
	if sig.HasThinking || !sig.HasVisibleText || !sig.ReasoningExpected {
		t.Fatalf("signal misread: %+v", sig)
	}
	if got := ClassifyQualityHold(sig, 32); got != QualityWithhold {
		t.Fatalf("effortful intent-statement verdict = %s, want withhold (signals: %+v)", got, sig)
	}

	// Same stream on a request without an explicit effort still delivers.
	state2 := &qualityScanState{protocol: qualityProtocolResponses}
	for _, f := range fixtures {
		ObserveQualityChunk(state2, []byte(f))
	}
	if got := ClassifyQualityHold(state2.signals(), 32); got != QualityDeliver {
		t.Fatalf("casual short answer verdict = %s, want deliver", got)
	}
}

// A withheld stream must surface its held SSE prefix as a diagnostic sample:
// the human-review pipeline depends on the excerpt being returned.
func TestWithheldStreamReturnsHeldSample(t *testing.T) {
	state := &qualityScanState{protocol: qualityProtocolResponses, reasoningExpected: true}
	fixtures := []string{
		`data: {"type":"response.created","response":{"id":"resp_1"}}` + "\n",
		`data: {"type":"response.output_text.delta","delta":"继续打 OA 和心理系统。"}` + "\n",
		`data: {"type":"response.completed","response":{"id":"resp_1","usage":{"input_tokens":152822,"output_tokens":17,"total_tokens":152839,"output_tokens_details":{"reasoning_tokens":0}}}}` + "\n",
		"data: [DONE]\n",
	}
	var held bytes.Buffer
	for _, f := range fixtures {
		held.WriteString(f)
		ObserveQualityChunk(state, []byte(f))
	}
	sig := state.signals()
	if got := ClassifyQualityHold(sig, 32); got != QualityWithhold {
		t.Fatalf("verdict = %s, want withhold", got)
	}
	sample := heldSampleBytes(&held)
	if len(sample) == 0 {
		t.Fatal("heldSampleBytes returned empty for a withheld stream")
	}
	if !bytes.Contains(sample, []byte("继续打 OA")) {
		t.Fatalf("sample does not contain the response text: %q", sample)
	}

	// Bound: a huge held prefix keeps the TAIL (deltas and usage arrive
	// last; the response.created preamble alone can exceed the limit).
	var big bytes.Buffer
	big.Write(bytes.Repeat([]byte("x"), qualityHeldSampleLimit+4096))
	big.WriteString("TAIL-MARKER")
	capped := heldSampleBytes(&big)
	if len(capped) != qualityHeldSampleLimit {
		t.Fatalf("capped sample = %d bytes, want %d", len(capped), qualityHeldSampleLimit)
	}
	if !bytes.HasSuffix(capped, []byte("TAIL-MARKER")) {
		t.Fatal("capped sample must keep the tail of the held prefix")
	}
}
