package gateway

import "testing"

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
