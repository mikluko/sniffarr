package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	sniffarrv1 "sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1"
	"sarak.sh/mikluko/sniffarr/internal/apis/sniffarr/v1/sniffarrv1connect"
)

type searchStub struct {
	sniffarrv1connect.UnimplementedSearchServiceHandler
}

func (searchStub) Search(_ context.Context, req *connect.Request[sniffarrv1.SearchRequest]) (*connect.Response[sniffarrv1.SearchResponse], error) {
	var rs []*sniffarrv1.Release
	for i := range req.Msg.GetLimit() {
		rs = append(rs, &sniffarrv1.Release{Name: req.Msg.GetQuery(), Size: int64(i)})
	}
	return connect.NewResponse(&sniffarrv1.SearchResponse{Releases: rs}), nil
}

type ingestStub struct {
	sniffarrv1connect.UnimplementedIngestServiceHandler
}

func (ingestStub) HashKey(context.Context, *connect.Request[sniffarrv1.HashKeyRequest]) (*connect.Response[sniffarrv1.HashKeyResponse], error) {
	return connect.NewResponse(&sniffarrv1.HashKeyResponse{Key: []byte("0123456789abcdef")}), nil
}

// serve starts an h2c-only API with the stubs.
func serve(t *testing.T) string {
	mux := http.NewServeMux()
	mux.Handle(sniffarrv1connect.NewSearchServiceHandler(searchStub{}))
	mux.Handle(sniffarrv1connect.NewIngestServiceHandler(ingestStub{}))
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			http.Error(w, "want HTTP/2", http.StatusHTTPVersionNotSupported)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	var p http.Protocols
	p.SetHTTP1(true)
	p.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &p
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestResolve(t *testing.T) {
	for _, name := range []string{"SearchService/Search", "sniffarr.v1.SearchService/Search", "/sniffarr.v1.SearchService/Search"} {
		md, err := resolve(name)
		require.NoError(t, err, name)
		require.Equal(t, "sniffarr.v1.SearchService/Search", procedure(md))
		require.Equal(t, "sniffarr.v1.SearchRequest", string(md.Input().FullName()))
	}
	for name, want := range map[string]string{
		"Search":                 "want <Service>/<Method>",
		"SearchService/":         "want <Service>/<Method>",
		"NoService/Search":       "unknown service sniffarr.v1.NoService",
		"SearchService/Nope":     "unknown method sniffarr.v1.SearchService/Nope",
		"sniffarr.v1.Release/X":  "sniffarr.v1.Release is not a service",
		"IngestService/Sight":    "client streaming: rpc calls unary methods only",
		"IngestService/Fetch":    "bidi streaming: rpc calls unary methods only",
		"other.v1.Service/Thing": "unknown service other.v1.Service",
	} {
		_, err := resolve(name)
		require.ErrorContains(t, err, want, name)
	}
}

func TestMethods(t *testing.T) {
	ms := methods()
	require.Contains(t, ms, "sniffarr.v1.BrowseService/Home")
	require.Contains(t, ms, "sniffarr.v1.IngestService/Fetch (bidi streaming)")
	require.Contains(t, ms, "sniffarr.v1.IngestService/HashKey")
	require.IsNonDecreasing(t, ms)
}

func TestRequestBody(t *testing.T) {
	b, err := requestBody([]string{`{"query":"x"}`}, strings.NewReader("ignored"))
	require.NoError(t, err)
	require.Equal(t, `{"query":"x"}`, string(b))
	for _, args := range [][]string{{"-"}, nil} {
		b, err = requestBody(args, strings.NewReader(`{"limit":2}`))
		require.NoError(t, err)
		require.Equal(t, `{"limit":2}`, string(b))
	}
}

func TestNewRequest(t *testing.T) {
	md, err := resolve("SearchService/Search")
	require.NoError(t, err)

	m, err := newRequest(md, []byte(`{"query":"House of the Dragon","limit":5,"kinds":["KIND_SERIES"]}`))
	require.NoError(t, err)
	got := &sniffarrv1.SearchRequest{}
	b, err := protojson.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, protojson.Unmarshal(b, got))
	require.Equal(t, "House of the Dragon", got.GetQuery())
	require.Equal(t, int32(5), got.GetLimit())
	require.Equal(t, []sniffarrv1.Kind{sniffarrv1.Kind_KIND_SERIES}, got.GetKinds())

	m, err = newRequest(md, []byte(" \n"))
	require.NoError(t, err)
	require.Zero(t, proto.Size(m))

	_, err = newRequest(md, []byte(`{"nope":1}`))
	require.ErrorContains(t, err, "request sniffarr.v1.SearchRequest")
}

func TestRun(t *testing.T) {
	url := serve(t)
	var out, errOut bytes.Buffer
	err := run(t.Context(), []string{"-v", "--url", url, "SearchService/Search", `{"query":"q","limit":2}`},
		strings.NewReader(""), &out, &errOut)
	require.NoError(t, err)
	require.Contains(t, out.String(), "\n  \"releases\":", "multiline, indented")
	got := &sniffarrv1.SearchResponse{}
	require.NoError(t, protojson.Unmarshal(out.Bytes(), got))
	require.Len(t, got.GetReleases(), 2)
	require.Equal(t, "q", got.GetReleases()[1].GetName())
	require.Equal(t, int64(1), got.GetReleases()[1].GetSize())
	require.Contains(t, errOut.String(), "method sniffarr.v1.SearchService/Search\n")
	require.Contains(t, errOut.String(), "url "+url+"/sniffarr.v1.SearchService/Search\n")
	require.Contains(t, errOut.String(), "time ")
}

func TestRunStdin(t *testing.T) {
	url := serve(t)
	var out bytes.Buffer
	err := run(t.Context(), []string{"--url", url, "SearchService/Search", "-"}, strings.NewReader(`{"limit":3}`), &out, &bytes.Buffer{})
	require.NoError(t, err)
	got := &sniffarrv1.SearchResponse{}
	require.NoError(t, protojson.Unmarshal(out.Bytes(), got))
	require.Len(t, got.GetReleases(), 3)
}

func TestRunEmptyRequest(t *testing.T) {
	url := serve(t)
	var out bytes.Buffer
	err := run(t.Context(), []string{"--url", url, "IngestService/HashKey"}, strings.NewReader(""), &out, &bytes.Buffer{})
	require.NoError(t, err)
	got := &sniffarrv1.HashKeyResponse{}
	require.NoError(t, protojson.Unmarshal(out.Bytes(), got))
	require.Equal(t, []byte("0123456789abcdef"), got.GetKey())
}

func TestRunError(t *testing.T) {
	url := serve(t)
	err := run(t.Context(), []string{"--url", url, "SearchService/Get"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	require.Equal(t, connect.CodeUnimplemented, connect.CodeOf(err))
}

func TestRunList(t *testing.T) {
	for _, args := range [][]string{{"-l"}, {"list"}} {
		var out bytes.Buffer
		require.NoError(t, run(t.Context(), args, strings.NewReader(""), &out, &bytes.Buffer{}))
		require.Equal(t, strings.Join(methods(), "\n")+"\n", out.String())
	}
}

func TestRunUsage(t *testing.T) {
	err := run(t.Context(), []string{"--bogus"}, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	require.ErrorIs(t, err, errUsage)
	err = run(t.Context(), nil, strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	require.ErrorContains(t, err, "want <Service>/<Method>")
}
