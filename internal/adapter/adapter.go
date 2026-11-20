// 本文件定义供应商适配器与中间 LLM 模型之间的边界。
//
// Package adapter 定义供应商适配器与中间 LLM 模型之间的边界。
package adapter

import (
	"context"

	"github.com/leookun/devin-2api/internal/llm"
)

// ModelInfo 是对外暴露的模型目录条目（OpenAI /v1/models 形状）。
type ModelInfo struct {
	// ID 是模型标识（OpenAI model id / Devin model_uid）。
	ID string
	// Created 是目录条目的 Unix 秒时间戳；未知时可为 0。
	Created int64
	// OwnedBy 是模型归属方展示名。
	OwnedBy string
	// SupportsImages 表示该模型是否支持多模态图片输入；目录未知时为 false。
	SupportsImages bool
}

// Adapter 将供应商无关的请求上下文转换为具体供应商调用，并返回有序响应流。
type Adapter interface {
	// Stream 开始一次或多次助手响应的流式生成。
	Stream(context.Context, llm.RequestMessages) (llm.ResponseStream, error)
	// ListModels 返回当前账号可用的模型目录；失败时返回错误。
	ListModels(context.Context) ([]ModelInfo, error)
}

// AudioTranscriber 是可选能力：实现它的适配器可把音频字节转写为文本，
// 供 /v1/audio/transcriptions 使用；未实现的适配器返回 503。
type AudioTranscriber interface {
	// TranscribeAudio 把完整音频字节流（wav/mp3/m4a 等）转写为文本。
	TranscribeAudio(context.Context, []byte) (string, error)
}
