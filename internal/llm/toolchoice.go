// 本文件把 OpenAI/Anthropic 两种 tool_choice 表示归一化为 llm.ToolChoice。
package llm

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ParseOpenAIToolChoice 解析 OpenAI 的 tool_choice 字段：
// "auto" | "none" | "required" | {"type":"function","function":{"name":"x"}}
func ParseOpenAIToolChoice(raw json.RawMessage) (*ToolChoice, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var option string
	if err := json.Unmarshal(raw, &option); err == nil {
		switch option {
		case "auto", "none", "required":
			return &ToolChoice{Mode: ToolChoiceMode(option)}, nil
		default:
			return nil, fmt.Errorf("unknown tool_choice %q", option)
		}
	}
	var object struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("tool_choice must be a string or object: %w", err)
	}
	name := object.Function.Name
	if name == "" {
		name = object.Name
	}
	if name == "" {
		return nil, errors.New("tool_choice object requires a function name")
	}
	return &ToolChoice{Mode: ToolChoiceNamed, ToolName: name}, nil
}

// ParseAnthropicToolChoice 解析 Anthropic 的 tool_choice 字段：
// {"type":"auto"|"any"|"none"|"tool", "name":"x", "disable_parallel_tool_use":bool}
// 返回值第二位是 disable_parallel_tool_use。
func ParseAnthropicToolChoice(raw json.RawMessage) (*ToolChoice, bool, error) {
	if len(raw) == 0 {
		return nil, false, nil
	}
	var object struct {
		Type                   string `json:"type"`
		Name                   string `json:"name"`
		DisableParallelToolUse bool   `json:"disable_parallel_tool_use"`
	}
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, false, fmt.Errorf("tool_choice must be an object: %w", err)
	}
	var choice *ToolChoice
	switch object.Type {
	case "auto":
		choice = &ToolChoice{Mode: ToolChoiceAuto}
	case "any":
		choice = &ToolChoice{Mode: ToolChoiceRequired}
	case "none":
		choice = &ToolChoice{Mode: ToolChoiceNone}
	case "tool":
		if object.Name == "" {
			return nil, false, errors.New("tool_choice type tool requires a name")
		}
		choice = &ToolChoice{Mode: ToolChoiceNamed, ToolName: object.Name}
	default:
		return nil, false, fmt.Errorf("unknown tool_choice type %q", object.Type)
	}
	return choice, object.DisableParallelToolUse, nil
}
