package ui

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseLanguages(t *testing.T) {
	for _, c := range []struct {
		spec string
		want []string
	}{
		{"", []string{"en"}},
		{"  ", []string{"en"}},
		{"en", []string{"en"}},
		{"C", []string{"en"}},
		{"POSIX", []string{"en"}},
		{"ru, en", []string{"ru", "en"}},
		{"EN,ru,C", []string{"en", "ru"}},
	} {
		got, err := ParseLanguages(c.spec)
		require.NoError(t, err, c.spec)
		require.Equal(t, c.want, got, c.spec)
	}
	for _, spec := range []string{"de", "en,", "en,,ru", "c"} {
		_, err := ParseLanguages(spec)
		require.Error(t, err, spec)
	}
	got, _ := ParseLanguages("")
	got[0] = "ru"
	require.Equal(t, []string{"en"}, DefaultLanguages, "the default is not shared with a caller")
}

func TestLang(t *testing.T) {
	lang := func(target string, supported []string, locale string) string {
		return Lang(httptest.NewRecorder(), httptest.NewRequest("GET", target, nil), supported, locale)
	}
	require.Equal(t, "en", lang("/?lang=ru", []string{"en"}, "ru-RU"), "a single language is the only answer")
	require.Equal(t, "ru", lang("/", []string{"ru", "en"}, ""), "the first is the default")
	require.Equal(t, "en", lang("/", []string{"ru", "en"}, "C"))
	require.Equal(t, "ru", lang("/", []string{"en", "ru"}, "ru_RU.UTF-8"))
	require.Equal(t, "en", lang("/?lang=en", []string{"ru", "en"}, "ru"), "a pick beats the locale")
}
