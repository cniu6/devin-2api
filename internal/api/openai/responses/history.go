// 本文件实现 previous_response_id 的历史补齐。
//
// 为什么需要：Responses 契约里服务端持有会话，客户端只发本轮新消息 + 上轮 id。
// 改造前该项目把 previous_response_id 解析出来后无处消费（全仓只有定义与赋值），
// 于是链式客户端每轮只有一条消息到达上游，模型盲答且不报错——最坏的一类缺陷：
// 输出流利、无上下文、调用方无从诊断。
//
// 本文件只做「补齐」，不做「存储」：存储见 internal/responsestore。
package responses

import (
	"github.com/leookun/devin-2api/internal/llm"
)

// HistoryResolution 描述 WithHistory 做了什么决定，供调用方记录日志。
type HistoryResolution struct {
	// Injected 表示历史确实被前置注入。
	Injected bool
	// HistoryCount 是被注入的历史消息条数。
	HistoryCount int
	// ClientSuppliedFullHistory 表示请求本身已带多轮消息，因此不需要注入。
	ClientSuppliedFullHistory bool
	// UnknownID 表示 previous_response_id 无法解析（不存在/过期/跨租户）。
	UnknownID bool
}

// WithHistory 在需要时把 previous_response_id 指向的历史前插到本轮请求之前。
//
// 兼容性策略（README 明确保证 "Each request must still carry the full message
// history; previous_response_id is accepted but currently does not enable
// server-side state retention"）：
//
//   - previousResponseID 为空 —— 原样返回。
//   - 请求已自带多轮历史（len(Messages) > 1）—— **不注入**，原样返回。
//     现有客户端全都这样用；对它们而言本次改动完全无感，也不会因为带了一个
//     早已过期的 id 而突然收到 400。
//   - 请求只有单条消息（真正的"只发本轮"链式调用）且 id 可解析 —— 前置注入历史。
//   - 请求只有单条消息但 id 无法解析 —— **不报错**，原样返回并把 UnknownID 置位。
//     选择不 fail-loud 的理由：这是兼容性优先。若在此返回 400，任何带着历史
//     遗留 id 的单轮请求都会被打断，而它们改造前是能正常工作的。调用方可据
//     UnknownID 记一条日志，把"本该补上却没补上"变成可观测事实，而不是静默。
func WithHistory(
	context llm.RequestMessages,
	previousResponseID string,
	caller string,
	lookup func(caller, id string) ([]llm.Message, bool),
) (llm.RequestMessages, HistoryResolution, error) {
	resolution := HistoryResolution{}

	if previousResponseID == "" {
		return context, resolution, nil
	}
	// 客户端已自带完整历史：尊重它，不叠加服务端状态。
	if len(context.Messages) > 1 {
		resolution.ClientSuppliedFullHistory = true
		return context, resolution, nil
	}
	if lookup == nil {
		resolution.UnknownID = true
		return context, resolution, nil
	}
	history, found := lookup(caller, previousResponseID)
	if !found {
		resolution.UnknownID = true
		return context, resolution, nil
	}

	// 历史在前、本轮在后。预分配精确容量，避免两条切片共享底层数组。
	merged := make([]llm.Message, 0, len(history)+len(context.Messages))
	merged = append(merged, history...)
	merged = append(merged, context.Messages...)
	context.Messages = merged

	resolution.Injected = true
	resolution.HistoryCount = len(history)
	return context, resolution, nil
}
