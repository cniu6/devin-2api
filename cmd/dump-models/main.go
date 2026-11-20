package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	devinproto "local/devinproto"
	"local/devinproto/devinprotoconnect"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func main() {
	// 从环境变量读取 Devin session token，避免把真实 token 写进代码仓库
	token := os.Getenv("DEVIN_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "请设置环境变量 DEVIN_TOKEN")
		os.Exit(1)
	}
	baseURL := "https://server.codeium.com"
	transport := &authTransport{base: http.DefaultTransport, token: token}
	httpClient := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	client := devinprotoconnect.NewApiServerServiceClient(httpClient, baseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := client.GetCascadeModelConfigs(ctx, connect.NewRequest(&devinproto.GetCascadeModelConfigsRequest{
		Metadata: &devinproto.ExaCodeiumCommonPb_Metadata{
			ApiKey:           proto.String(token),
			ExtensionName:    proto.String("chisel"),
			ExtensionVersion: proto.String("3000.2.17"),
			IdeName:          proto.String("chisel"),
			IdeVersion:       proto.String("3000.2.17"),
			Locale:           proto.String("en"),
			Os:               proto.String("win"),
		},
	}))
	if err != nil {
		fmt.Println("ERR", err)
		return
	}

	type row struct {
		UID               string          `json:"uid"`
		Label             string          `json:"label"`
		Multiplier        float32         `json:"multiplier"`
		CostTier          string          `json:"cost_tier"`
		PricingType       string          `json:"pricing_type"`
		Provider          string          `json:"provider"`
		APIProvider       string          `json:"api_provider"`
		Promo             any             `json:"promo,omitempty"`
		Dimensions        any             `json:"dimensions,omitempty"`
		MaxTokens         int32           `json:"max_tokens"`
		Description       string          `json:"description,omitempty"`
		IsPremium         bool            `json:"is_premium"`
		IsBeta            bool            `json:"is_beta"`
		IsNew             bool            `json:"is_new"`
		SupportsImages    bool            `json:"supports_images"`
		IsRecommended     bool            `json:"is_recommended"`
		IsCapacityLimited bool            `json:"is_capacity_limited"`
		Fast              any             `json:"fast,omitempty"`
		Family            any             `json:"family,omitempty"`
		Raw               json.RawMessage `json:"raw,omitempty"`
	}

	var rows []row
	emptyMult := 0
	withDims := 0
	withPromo := 0
	rawCount := 0
	for i, c := range resp.Msg.GetClientModelConfigs() {
		uid := c.GetModelUid()
		if uid == "" && c.GetModelOrAlias() != nil {
			uid = c.GetModelOrAlias().GetModelUid()
		}
		if c.GetCreditMultiplier() == 0 {
			emptyMult++
		}
		if len(c.GetModelDimensions()) > 0 {
			withDims++
		}
		promoActive := c.GetPromoStatus() != nil && c.GetPromoStatus().GetIsActive()
		if promoActive {
			withPromo++
		}

		r := row{
			UID:               uid,
			Label:             c.GetLabel(),
			Multiplier:        c.GetCreditMultiplier(),
			CostTier:          c.GetModelCostTier().String(),
			PricingType:       c.GetPricingType().String(),
			Provider:          c.GetProvider().String(),
			APIProvider:       c.GetApiProvider().String(),
			MaxTokens:         c.GetMaxTokens(),
			Description:       c.GetDescription(),
			IsPremium:         c.GetIsPremium(),
			IsBeta:            c.GetIsBeta(),
			IsNew:             c.GetIsNew(),
			SupportsImages:    c.GetSupportsImages(),
			IsRecommended:     c.GetIsRecommended(),
			IsCapacityLimited: c.GetIsCapacityLimited(),
		}
		if promoActive {
			ps := c.GetPromoStatus()
			r.Promo = map[string]any{
				"label":     ps.GetLabel(),
				"is_active": ps.GetIsActive(),
				"end_date":  ps.GetEndDate().String(),
			}
		}
		if fs := c.GetFastStatus(); fs != nil && fs.GetIsActive() {
			r.Fast = map[string]any{"active": true, "tooltip": fs.GetTooltip()}
		}
		if len(c.GetModelDimensions()) > 0 {
			var dims []map[string]any
			for _, d := range c.GetModelDimensions() {
				dims = append(dims, map[string]any{
					"label":       d.GetLabel(),
					"value":       d.GetValue(),
					"denominator": d.GetDenominator(),
					"min":         d.GetMinRange(),
					"max":         d.GetMaxRange(),
					"kind":        d.GetKind().String(),
					"info":        d.GetInfo(),
				})
			}
			r.Dimensions = dims
		}
		if fm := c.GetModelFamilyMetadata(); fm != nil {
			r.Family = map[string]any{
				"label":      fm.GetModelFamilyLabel(),
				"is_default": fm.GetIsDefaultModelInFamily(),
			}
		}
		interesting := i < 2 || c.GetCreditMultiplier() == 0 || promoActive || len(c.GetModelDimensions()) > 0
		if interesting && rawCount < 12 {
			b, _ := protojson.MarshalOptions{EmitUnpopulated: false}.Marshal(c)
			r.Raw = b
			rawCount++
		}
		rows = append(rows, r)
	}

	var sorts []map[string]any
	for _, s := range resp.Msg.GetClientModelSorts() {
		var groups []map[string]any
		for _, g := range s.GetGroups() {
			groups = append(groups, map[string]any{
				"name":         g.GetGroupName(),
				"model_labels": g.GetModelLabels(),
			})
		}
		sorts = append(sorts, map[string]any{
			"name":           s.GetName(),
			"display_metric": s.GetDisplayMetric(),
			"groups":         groups,
		})
	}

	out := map[string]any{
		"total":            len(rows),
		"empty_multiplier": emptyMult,
		"with_dimensions":  withDims,
		"with_promo":       withPromo,
		"model_sorts":      sorts,
		"rows":             rows,
	}
	f, _ := os.Create("outputs/model-dump.json")
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
	fmt.Printf("total=%d empty_mult=%d dims=%d promo=%d\n", len(rows), emptyMult, withDims, withPromo)
}

type authTransport struct {
	base  http.RoundTripper
	token string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Basic "+t.token+"-"+t.token)
	return t.base.RoundTrip(clone)
}
