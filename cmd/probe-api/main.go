// 本文件是一次性探测工具：用真实 token 探测上游非 chat RPC 的可用性。
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"connectrpc.com/connect"
	devinproto "local/devinproto"
	"local/devinproto/devinprotoconnect"

	"google.golang.org/protobuf/proto"
)

// authTransport 与 dump-models 一致：Basic <token>-<token>。
type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Basic "+t.token+"-"+t.token)
	return t.base.RoundTrip(clone)
}

func main() {
	token := os.Getenv("DEVIN_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "DEVIN_TOKEN required")
		os.Exit(1)
	}
	baseURL := os.Getenv("DEVIN_BASE_URL")
	if baseURL == "" {
		baseURL = "https://server.codeium.com"
	}
	client := devinprotoconnect.NewApiServerServiceClient(
		&http.Client{Transport: &authTransport{base: http.DefaultTransport, token: token}, Timeout: 120 * time.Second},
		baseURL)
	meta := &devinproto.ExaCodeiumCommonPb_Metadata{
		ApiKey:           proto.String(token),
		ExtensionName:    proto.String("devin-2api-probe"),
		ExtensionVersion: proto.String("0.1.0"),
		IdeName:          proto.String("devin-2api-probe"),
		IdeVersion:       proto.String("0.1.0"),
		Locale:           proto.String("en"),
		Os:               proto.String("win"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()

	// 1. 命令模型目录（补全/提交信息等命令用模型）
	cmdResp, err := client.GetCommandModelConfigs(ctx, connect.NewRequest(&devinproto.GetCommandModelConfigsRequest{Metadata: meta}))
	if err != nil {
		fmt.Println("COMMAND_CONFIGS ERR:", err)
	} else {
		fmt.Println("COMMAND_CONFIGS:", len(cmdResp.Msg.GetClientModelConfigs()))
		for _, c := range cmdResp.Msg.GetClientModelConfigs() {
			fmt.Println("  ", c.GetModelUid(), "|", c.GetLabel(), "|", c.GetProvider(), "|", c.GetCreditMultiplier())
		}
	}

	// 1b. CLI 目录 + ForSite 目录：探测是否有隐藏/额外模型
	cliResp, err := client.GetCliModelConfigs(ctx, connect.NewRequest(&devinproto.GetCliModelConfigsRequest{Metadata: meta}))
	if err != nil {
		fmt.Println("CLI_CONFIGS ERR:", err)
	} else {
		fmt.Println("CLI_CONFIGS:", len(cliResp.Msg.GetClientModelConfigs()))
		for _, c := range cliResp.Msg.GetClientModelConfigs() {
			fmt.Println("  ", c.GetModelUid(), "|", c.GetLabel(), "|", c.GetProvider(), "|", c.GetCreditMultiplier(), "| disabled:", c.GetDisabled())
		}
	}
	siteResp, err := client.GetCascadeModelConfigsForSite(ctx, connect.NewRequest(&devinproto.GetCascadeModelConfigsForSiteRequest{
		ApiKey:           proto.String(token),
		IdeName:          proto.String("devin-2api-probe"),
		IdeVersion:       proto.String("0.1.0"),
		ExtensionVersion: proto.String("0.1.0"),
	}))
	if err != nil {
		fmt.Println("SITE_CONFIGS ERR:", err)
	} else {
		fmt.Println("SITE_CONFIGS:", len(siteResp.Msg.GetClientModelConfigs()))
	}

	// 2. 嵌入
	embResp, err := client.GetEmbeddings(ctx, connect.NewRequest(&devinproto.GetEmbeddingsRequest{
		Request: &devinproto.ExaCodeiumCommonPb_EmbeddingsRequest{
			Prompts:  []string{"hello world", "func main() {}"},
			Model:    devinproto.ExaCodeiumCommonPb_Model_ExaCodeiumCommonPb_Model_MODEL_EMBED_6591.Enum(),
			Priority: devinproto.ExaCodeiumCommonPb_EmbeddingPriority_ExaCodeiumCommonPb_EmbeddingPriority_EMBEDDING_PRIORITY_HIGH.Enum(),
		},
		EmbeddingModel: devinproto.ExaCodeiumCommonPb_Model_ExaCodeiumCommonPb_Model_MODEL_EMBED_6591.Enum(),
	}))
	if err != nil {
		fmt.Println("EMBEDDINGS ERR:", err)
	} else {
		embs := embResp.Msg.GetResponse().GetEmbeddings()
		fmt.Println("EMBEDDINGS:", len(embs), "vectors")
		for i, e := range embs {
			fmt.Printf("   [%d] dim=%d first=%.4f\n", i, len(e.GetValues()), e.GetValues()[0])
		}
	}

	// 3. 补全（FIM）：补 language 枚举 + 行尾标记
	compResp, err := client.GetCompletions(ctx, connect.NewRequest(&devinproto.GetCompletionsRequest{
		Metadata: meta,
		Request: &devinproto.ExaCodeiumCommonPb_CompletionsRequest{
			Prompt:          proto.String("def fibonacci(n):\n    "),
			EditorLanguage:  proto.String("python"),
			Language:        devinproto.ExaCodeiumCommonPb_Language_ExaCodeiumCommonPb_Language_LANGUAGE_PYTHON.Enum(),
			Model:           devinproto.ExaCodeiumCommonPb_Model_ExaCodeiumCommonPb_Model_MODEL_TAB_ARMADILLO.Enum(),
			HasLineSuffix:   proto.Bool(false),
			ShouldInlineFim: proto.Bool(true),
			Uid:             proto.String("probe-uid-1"),
			Configuration: &devinproto.ExaCodeiumCommonPb_CompletionConfiguration{
				NumCompletions: proto.Uint64(3),
				MaxTokens:      proto.Uint64(64),
			},
		},
	}))
	if err != nil {
		fmt.Println("COMPLETIONS ERR:", err)
	} else {
		comps := compResp.Msg.GetCompletionResponse().GetCompletions()
		fmt.Println("COMPLETIONS:", len(comps))
		for i, c := range comps {
			fmt.Printf("   [%d] stop=%v text=%q\n", i, c.GetStopReason(), c.GetText())
		}
	}

	// 4. 语音转写（发一段静音 wav 探测接口活性）
	wav := silentWav()
	trResp, err := client.GetTranscription(ctx, connect.NewRequest(&devinproto.GetTranscriptionRequest{
		Metadata:  meta,
		AudioData: wav,
	}))
	if err != nil {
		fmt.Println("TRANSCRIPTION ERR:", err)
	} else {
		fmt.Printf("TRANSCRIPTION: %q\n", trResp.Msg.GetTranscribedText())
	}
}

// silentWav 生成 1 秒 16kHz 单声道静音 PCM WAV。
func silentWav() []byte {
	dataSize := 16000 * 2
	wav := make([]byte, 44+dataSize)
	copy(wav[0:4], "RIFF")
	put32(wav[4:], uint32(36+dataSize))
	copy(wav[8:12], "WAVE")
	copy(wav[12:16], "fmt ")
	put32(wav[16:], 16)
	put16(wav[20:], 1)
	put16(wav[22:], 1)
	put32(wav[24:], 16000)
	put32(wav[28:], 32000)
	put16(wav[32:], 2)
	put16(wav[34:], 16)
	copy(wav[36:40], "data")
	put32(wav[40:], uint32(dataSize))
	return wav
}

func put32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func put16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }
