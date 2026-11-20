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

	// 以下字段来自上游目录（该适配器支持时填写），供 /v1/models 与面板透出。

	// Label 是上游分配的显示名。
	Label string
	// Description 是上游分配的模型描述。
	Description string
	// Family 是上游模型系列标签。
	Family string
	// Provider 是上游计费/渠道提供方枚举名（如 ANTHROPIC/GOOGLE/META）。
	Provider string
	// APIProvider 是上游 API 提供方枚举名。
	APIProvider string
	// CostTier 是上游定价等级：free/low/medium/high。
	CostTier string
	// PricingType 是上游计费类型枚举短名（如 STATIC_CREDIT/BYOK）。
	PricingType string
	// CreditMultiplier 是上游每请求 credit 倍率；0 表示未知或免费。
	CreditMultiplier float64
	// MaxTokens 是上游宣称的上下文窗口大小。
	MaxTokens int64
	// BetaWarning 是 beta 模型的上游警告文案。
	BetaWarning string
	// Promo 非空表示该模型有进行中的促销。
	Promo *ModelPromo
	// IsBeta/IsNew/IsRecommended/IsPremium/IsCapacityLimited/Disabled 是上游目录标记。
	IsBeta            bool
	IsNew             bool
	IsRecommended     bool
	IsPremium         bool
	IsCapacityLimited bool
	Disabled          bool
	// IsCommand 表示来自命令模型目录（commit 信息等），不在 chat picker 中。
	IsCommand bool
}

// ModelPromo 描述一个进行中的模型促销。
type ModelPromo struct {
	// Label 是上游促销文案（如 "83% off"），可为空。
	Label string
	// EndDate 是促销截止的 Unix 秒时间戳；0 表示未公布。
	EndDate int64
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
