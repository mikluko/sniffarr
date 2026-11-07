package api

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mikluko/sniffarr/internal/classify"
)

func TestClassifierEndpoint(t *testing.T) {
	const own = "http://laya:8000/v1/systemone"
	tests := []struct {
		name string
		cfg  Config
		want endpoint
		on   bool
	}{
		{name: "nothing set", cfg: Config{DailyTokens: 100},
			want: endpoint{url: classify.DefaultURL, dailyTokens: 100, setting: "TYPESAFE_API_KEY"}},
		{name: "typesafe key", cfg: Config{TypesafeKey: "ts", DailyTokens: 100},
			want: endpoint{url: classify.DefaultURL, key: "ts", dailyTokens: 100, setting: "TYPESAFE_API_KEY"}, on: true},
		{name: "typesafe's endpoint named", cfg: Config{TypesafeKey: "ts", ClassifyURL: classify.DefaultURL, DailyTokens: 100},
			want: endpoint{url: classify.DefaultURL, key: "ts", dailyTokens: 100, setting: "TYPESAFE_API_KEY"}, on: true},
		{name: "own endpoint without a key", cfg: Config{ClassifyURL: own, DailyTokens: 100},
			want: endpoint{url: own, setting: "SNIFFARR_CLASSIFY_URL"}, on: true},
		{name: "own endpoint is not sent the typesafe key", cfg: Config{TypesafeKey: "ts", ClassifyURL: own, DailyTokens: 100},
			want: endpoint{url: own, setting: "SNIFFARR_CLASSIFY_URL"}, on: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, on := classifierEndpoint(tt.cfg)
			require.Equal(t, tt.want, e)
			require.Equal(t, tt.on, on)
		})
	}
}
