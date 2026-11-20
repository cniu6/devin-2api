package main

import (
	"context"
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

type tr struct{ token string }

func (t tr) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Basic "+t.token+"-"+t.token)
	return http.DefaultTransport.RoundTrip(r)
}

func main() {
	token := os.Getenv("DEVIN_TOKEN")
	client := devinprotoconnect.NewApiServerServiceClient(&http.Client{Transport: tr{token}, Timeout: 60 * time.Second}, "https://server.codeium.com")
	resp, err := client.GetCascadeModelConfigs(context.Background(), connect.NewRequest(&devinproto.GetCascadeModelConfigsRequest{Metadata: &devinproto.ExaCodeiumCommonPb_Metadata{
		ApiKey: proto.String(token), ExtensionName: proto.String("windsurf"), ExtensionVersion: proto.String("1.48.2"),
		IdeName: proto.String("windsurf"), IdeVersion: proto.String("1.48.2"), Locale: proto.String("en"), Os: proto.String("mac")}}))
	if err != nil {
		fmt.Println("ERR", err)
		return
	}
	withInfo := 0
	for i, c := range resp.Msg.GetClientModelConfigs() {
		if c.GetModelInfo() != nil {
			withInfo++
		}
		if i < 4 || c.GetModelUid() == "swe-2-high" {
			c.PromoStatus = nil
			c.ModelDimensions = nil
			b, _ := protojson.Marshal(c)
			fmt.Println(string(b))
		}
	}
	fmt.Println("total", len(resp.Msg.GetClientModelConfigs()), "with model_info", withInfo)
}
