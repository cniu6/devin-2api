// 本文件提供 OpenAI / Anthropic 兼容 API 共用的内容解码工具。
package common

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"regexp"
	"strings"

	"github.com/leookun/devin-2api/internal/llm"
)

// ErrImageShape 表示无法识别的图片值形态。
var ErrImageShape = errors.New("unrecognized image value shape")

// DecodeContent 把 JSON 字符串或 part 数组解码为中间内容块。
func DecodeContent(raw json.RawMessage) ([]llm.Content, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []llm.Content{llm.TextContent{Text: SanitizeText(text)}}, nil
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, fmt.Errorf("decode message content: %w", err)
	}
	content := make([]llm.Content, 0, len(parts))
	for index, part := range parts {
		var header struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(part, &header); err != nil {
			return nil, fmt.Errorf("content[%d]: %w", index, err)
		}
		switch header.Type {
		case "input_text", "output_text", "text":
			content = append(content, llm.TextContent{Text: SanitizeText(header.Text)})
		case "input_image", "image_url", "image":
			image, err := DecodeImagePart(part)
			if err != nil {
				return nil, fmt.Errorf("content[%d]: %w", index, err)
			}
			content = append(content, image)
		default:
			// 忽略未知 part，避免 IDE 额外字段整请求失败。
			continue
		}
	}
	return content, nil
}

// DecodeImagePart 兼容 OpenAI Responses / Chat Completions / Anthropic 常见图片 part 形态。
func DecodeImagePart(raw json.RawMessage) (llm.ImageContent, error) {
	var envelope struct {
		Type     string          `json:"type"`
		ImageURL json.RawMessage `json:"image_url"`
		Image    json.RawMessage `json:"image"`
		Source   json.RawMessage `json:"source"`
		FileID   string          `json:"file_id"`
		Detail   string          `json:"detail"`
		// 少数客户端把 data URL 直接放在 url / data 字段。
		URL  string `json:"url"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return llm.ImageContent{}, err
	}
	if envelope.FileID != "" {
		return llm.ImageContent{}, errors.New("file_id images are not supported; use base64 data URL in image_url")
	}

	candidates := []json.RawMessage{envelope.ImageURL, envelope.Image, envelope.Source}
	for _, candidate := range candidates {
		if len(bytes.TrimSpace(candidate)) == 0 || bytes.Equal(bytes.TrimSpace(candidate), []byte("null")) {
			continue
		}
		if image, err := DecodeImageValue(candidate); err == nil {
			return image, nil
		} else if !errors.Is(err, ErrImageShape) {
			return llm.ImageContent{}, err
		}
	}
	if envelope.URL != "" {
		return DecodeDataImage(envelope.URL)
	}
	if envelope.Data != "" {
		return DecodeDataImage(envelope.Data)
	}
	return llm.ImageContent{}, errors.New("image part missing image_url/url/data (base64 data URL required)")
}

// DecodeImageValue 解析 JSON 字符串或图片对象。
func DecodeImageValue(raw json.RawMessage) (llm.ImageContent, error) {
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return DecodeDataImage(asString)
	}
	var asObject struct {
		URL       string `json:"url"`
		Data      string `json:"data"`
		Base64    string `json:"base64"`
		B64JSON   string `json:"b64_json"`
		MIMEType  string `json:"mime_type"`
		MediaType string `json:"media_type"`
		Type      string `json:"type"` // anthropic source.type = base64
		Detail    string `json:"detail"`
		FileID    string `json:"file_id"`
	}
	if err := json.Unmarshal(raw, &asObject); err != nil {
		return llm.ImageContent{}, ErrImageShape
	}
	if asObject.FileID != "" {
		return llm.ImageContent{}, errors.New("file_id images are not supported; use base64 data URL")
	}
	if asObject.URL != "" {
		return DecodeDataImage(asObject.URL)
	}
	encoded := asObject.Data
	if encoded == "" {
		encoded = asObject.Base64
	}
	if encoded == "" {
		encoded = asObject.B64JSON
	}
	if encoded == "" {
		return llm.ImageContent{}, ErrImageShape
	}
	mimeType := asObject.MIMEType
	if mimeType == "" {
		mimeType = asObject.MediaType
	}
	if strings.HasPrefix(encoded, "data:") {
		return DecodeDataImage(encoded)
	}
	if mimeType == "" {
		mimeType = SniffImageMIME(encoded)
	}
	if mimeType == "" {
		return llm.ImageContent{}, errors.New("image base64 requires mime_type/media_type or data URL prefix")
	}
	return DecodeRawBase64(encoded, mimeType)
}

// DecodeDataImage 解析 data URL 或裸 base64 图片字符串。
func DecodeDataImage(value string) (llm.ImageContent, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return llm.ImageContent{}, errors.New("image url/data is empty")
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return llm.ImageContent{}, errors.New("http(s) image URLs are not fetched yet; embed as data:image/...;base64,...")
	}
	if !strings.HasPrefix(value, "data:") {
		// 纯 base64：尝试按魔数嗅探。
		if mimeType := SniffImageMIME(value); mimeType != "" {
			return DecodeRawBase64(value, mimeType)
		}
		return llm.ImageContent{}, errors.New("only data URL or raw base64 images are supported")
	}
	meta, encoded, ok := strings.Cut(value, ",")
	if !ok {
		return llm.ImageContent{}, errors.New("image must be a base64 data URL")
	}
	meta = strings.TrimPrefix(meta, "data:")
	// 允许 data:image/png;base64,xxx 与 data:image/png;charset=utf-8;base64,xxx
	isBase64 := strings.Contains(meta, ";base64") || !strings.Contains(meta, ";")
	if strings.Contains(meta, ";base64") {
		isBase64 = true
	}
	mimeType := meta
	if i := strings.Index(mimeType, ";"); i >= 0 {
		mimeType = mimeType[:i]
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		mimeType = "image/png"
	}
	if _, _, err := mime.ParseMediaType(mimeType); err != nil {
		return llm.ImageContent{}, fmt.Errorf("invalid image MIME type: %w", err)
	}
	if !isBase64 {
		return llm.ImageContent{}, errors.New("image data URL must be base64 encoded")
	}
	return DecodeRawBase64(encoded, mimeType)
}

// DecodeRawBase64 把 base64 字符串解码并返回中间图片内容块。
func DecodeRawBase64(encoded, mimeType string) (llm.ImageContent, error) {
	encoded = strings.TrimSpace(encoded)
	// 去掉空白/换行（部分客户端会折行）。
	encoded = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, encoded)
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		// URL-safe base64
		data, err = base64.URLEncoding.DecodeString(encoded)
		if err != nil {
			data, err = base64.RawStdEncoding.DecodeString(encoded)
			if err != nil {
				data, err = base64.RawURLEncoding.DecodeString(encoded)
			}
		}
		if err != nil {
			return llm.ImageContent{}, fmt.Errorf("decode image data: %w", err)
		}
	}
	if len(data) == 0 {
		return llm.ImageContent{}, errors.New("image data is empty")
	}
	// 上游按纯 base64 字符串接收，不带 data: 前缀。
	return llm.ImageContent{
		Data:     base64.StdEncoding.EncodeToString(data),
		MIMEType: mimeType,
	}, nil
}

// SniffImageMIME 通过 base64 解码后的文件魔数猜测图片 MIME 类型。
func SniffImageMIME(encoded string) string {
	encoded = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, encoded)
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(raw) < 4 {
		return ""
	}
	switch {
	case len(raw) >= 3 && raw[0] == 0xff && raw[1] == 0xd8 && raw[2] == 0xff:
		return "image/jpeg"
	case len(raw) >= 8 && raw[0] == 0x89 && raw[1] == 0x50 && raw[2] == 0x4e && raw[3] == 0x47:
		return "image/png"
	case len(raw) >= 6 && raw[0] == 0x47 && raw[1] == 0x49 && raw[2] == 0x46:
		return "image/gif"
	case len(raw) >= 12 && raw[0] == 0x52 && raw[1] == 0x49 && raw[2] == 0x46 && raw[3] == 0x46 &&
		raw[8] == 0x57 && raw[9] == 0x45 && raw[10] == 0x42 && raw[11] == 0x50:
		return "image/webp"
	default:
		return ""
	}
}

// RawOutputText 把工具输出 JSON 优先当字符串处理，非字符串时回退到原始 JSON 文本。
func RawOutputText(raw json.RawMessage) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", errors.New("function call output is required")
	}
	return string(raw), nil
}

// ContentText 从内容块中提取纯文本。
// codexPermissionsBlock 匹配 codex 发送的 <permissions instructions>...</permissions instructions> 块，
// 上游 Devin 的内容策略会因此拒绝请求。
var codexPermissionsBlock = regexp.MustCompile(`(?s)<permissions instructions>.*?</permissions instructions>`)

// SanitizeText 移除 codex system prompt 中可能触发上游内容策略的敏感块。
func SanitizeText(text string) string {
	return codexPermissionsBlock.ReplaceAllString(text, "")
}

func ContentText(content []llm.Content) string {
	var builder strings.Builder
	for _, block := range content {
		if text, ok := block.(llm.TextContent); ok {
			builder.WriteString(text.Text)
		}
	}
	return builder.String()
}

// HasCacheControl 判断消息 content 的任一内容块是否携带非空 cache_control
// （Anthropic 原生与 OpenRouter 风格的 OpenAI 兼容请求都使用该写法）。
func HasCacheControl(raw json.RawMessage) bool {
	var parts []struct {
		CacheControl json.RawMessage `json:"cache_control"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, part := range parts {
		trimmed := bytes.TrimSpace(part.CacheControl)
		if len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			return true
		}
	}
	return false
}

// ResponseFormatInstruction 把 OpenAI response_format / text.format 转为系统提示约束。
// Devin 上游没有结构化输出字段，只能以指令形式下发；text/空值返回空串。
func ResponseFormatInstruction(raw json.RawMessage) string {
	var format struct {
		Type       string          `json:"type"`
		Name       string          `json:"name"`
		Schema     json.RawMessage `json:"schema"`
		JSONSchema *struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
		} `json:"json_schema"`
	}
	if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &format) != nil {
		return ""
	}
	switch format.Type {
	case "json_object":
		return "Respond with a single valid JSON object only. Do not wrap it in markdown code fences or add any other text."
	case "json_schema":
		schema := format.Schema
		if format.JSONSchema != nil && len(format.JSONSchema.Schema) > 0 {
			schema = format.JSONSchema.Schema
		}
		if len(bytes.TrimSpace(schema)) == 0 {
			return "Respond with a single valid JSON object only. Do not wrap it in markdown code fences or add any other text."
		}
		return "Respond with a single valid JSON value that strictly conforms to this JSON Schema. Do not wrap it in markdown code fences or add any other text.\nJSON Schema:\n" + string(schema)
	default:
		return ""
	}
}

// AppendSystemInstruction 把附加约束追加到系统提示词末尾。
func AppendSystemInstruction(system, instruction string) string {
	if instruction == "" {
		return system
	}
	if system == "" {
		return instruction
	}
	return system + "\n\n" + instruction
}
