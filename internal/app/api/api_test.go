package api

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"sarak.sh/mikluko/sniffarr/internal/classify"
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
			want: endpoint{url: classify.DefaultURL, dailyTokens: 100, setting: "SNIFFARR_CLASSIFY_API_KEY"}},
		{name: "default endpoint named without a key", cfg: Config{ClassifyURL: classify.DefaultURL, DailyTokens: 100},
			want: endpoint{url: classify.DefaultURL, dailyTokens: 100, setting: "SNIFFARR_CLASSIFY_API_KEY"}},
		{name: "key", cfg: Config{ClassifyKey: "ck", DailyTokens: 100},
			want: endpoint{url: classify.DefaultURL, key: "ck", dailyTokens: 100, setting: "SNIFFARR_CLASSIFY_API_KEY"}, on: true},
		{name: "deprecated key", cfg: Config{TypesafeKey: "ts", DailyTokens: 100},
			want: endpoint{url: classify.DefaultURL, key: "ts", dailyTokens: 100, setting: "TYPESAFE_API_KEY"}, on: true},
		{name: "key over the deprecated one", cfg: Config{ClassifyKey: "ck", TypesafeKey: "ts", DailyTokens: 100},
			want: endpoint{url: classify.DefaultURL, key: "ck", dailyTokens: 100, setting: "SNIFFARR_CLASSIFY_API_KEY"}, on: true},
		{name: "default endpoint named with the deprecated key", cfg: Config{TypesafeKey: "ts", ClassifyURL: classify.DefaultURL, DailyTokens: 100},
			want: endpoint{url: classify.DefaultURL, key: "ts", dailyTokens: 100, setting: "TYPESAFE_API_KEY"}, on: true},
		{name: "own endpoint without a key", cfg: Config{ClassifyURL: own, DailyTokens: 100},
			want: endpoint{url: own, setting: "SNIFFARR_CLASSIFY_URL"}, on: true},
		{name: "own endpoint with its key", cfg: Config{ClassifyURL: own, ClassifyKey: "ck", DailyTokens: 100},
			want: endpoint{url: own, key: "ck", setting: "SNIFFARR_CLASSIFY_URL"}, on: true},
		{name: "own endpoint is not sent the deprecated key", cfg: Config{TypesafeKey: "ts", ClassifyURL: own, DailyTokens: 100},
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

func TestNewClassifiers(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	off := newClassifiers(Config{}, nil, log)
	require.Nil(t, off.classifier, "no key with the default endpoint")
	require.Nil(t, off.lazy)

	own := newClassifiers(Config{ClassifyURL: "http://laya:8000/v1/systemone", TypesafeKey: "ts", DailyTokens: 100}, nil, log)
	require.NotNil(t, own.classifier)
	client, ok := own.classifier.Model.(*classify.Client)
	require.True(t, ok)
	require.Equal(t, "http://laya:8000/v1/systemone", client.URL)
	require.Empty(t, client.Key)
	require.Zero(t, own.classifier.DailyTokens)
	require.Same(t, own.classifier, own.lazy.Classifier)
}

func TestNewChecker(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	require.Nil(t, newChecker(Config{}, log), "the check is off")
	require.NotNil(t, newChecker(Config{ReleaseCheck: true}, log))
}
