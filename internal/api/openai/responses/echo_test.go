// 本文件验证 Responses 响应体如实回显请求侧字段。
package responses

import (
	"encoding/json"
	"testing"
)

func TestEncodeResponseEchoesRequestFields(t *testing.T) {
	adapted, err := DecodeRequest([]byte(`{
		"model": "gpt-test",
		"instructions": "be terse",
		"input": "hi",
		"temperature": 0.3,
		"top_p": 0.7,
		"max_output_tokens": 256,
		"previous_response_id": "resp_prev",
		"parallel_tool_calls": false,
		"tool_choice": "none",
		"truncation": "auto",
		"metadata": {"env": "prod"},
		"tools": [{"type":"function","name":"fn","parameters":{"type":"object"}}]
	}`))
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if adapted.Context.Temperature == nil || *adapted.Context.Temperature != 0.3 {
		t.Fatalf("temperature not forwarded to context: %v", adapted.Context.Temperature)
	}
	if !adapted.Context.DisableParallelToolCalls {
		t.Fatal("parallel_tool_calls:false must set DisableParallelToolCalls")
	}
	if adapted.Context.ToolChoice == nil || adapted.Context.ToolChoice.Mode != "none" {
		t.Fatalf("tool_choice = %+v", adapted.Context.ToolChoice)
	}

	body, err := EncodeResponse(okMessage(), adapted.Options.Store, &adapted.Options.Echo)
	if err != nil {
		t.Fatalf("EncodeResponse: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["instructions"] != "be terse" ||
		got["previous_response_id"] != "resp_prev" ||
		got["temperature"] != 0.3 ||
		got["top_p"] != 0.7 ||
		got["max_output_tokens"] != float64(256) ||
		got["parallel_tool_calls"] != false ||
		got["tool_choice"] != "none" ||
		got["truncation"] != "auto" {
		t.Fatalf("echo mismatch: %+v", got)
	}
	meta, _ := got["metadata"].(map[string]any)
	if meta["env"] != "prod" {
		t.Fatalf("metadata = %v", got["metadata"])
	}
	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", got["tools"])
	}
}
