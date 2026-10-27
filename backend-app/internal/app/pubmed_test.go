package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPubMedSearchIncludesAbstractAndPreservesOrder(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	requests := []url.Values{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Query())
		mu.Unlock()
		switch r.URL.Path {
		case "/esearch.fcgi":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"esearchresult":{"count":"10001","idlist":["20","10"]}}`))
		case "/efetch.fcgi":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = w.Write([]byte(`<PubmedArticleSet>
<PubmedArticle><MedlineCitation><PMID>10</PMID><Article><ArticleTitle>Second &amp; useful</ArticleTitle><Abstract><AbstractText Label="BACKGROUND">Background text.</AbstractText><AbstractText>Result text.</AbstractText></Abstract><AuthorList><Author><LastName>Petrov</LastName><ForeName>Ivan</ForeName></Author></AuthorList><Journal><JournalIssue><PubDate><Year>2025</Year></PubDate></JournalIssue><Title>Clinical Journal</Title></Journal></Article></MedlineCitation><PubmedData><ArticleIdList><ArticleId IdType="doi">10.1/example</ArticleId></ArticleIdList></PubmedData></PubmedArticle>
<PubmedArticle><MedlineCitation><PMID>20</PMID><Article><ArticleTitle>First result</ArticleTitle><Abstract><AbstractText>Full abstract.</AbstractText></Abstract><AuthorList><Author><CollectiveName>Study Group</CollectiveName></Author></AuthorList><Journal><JournalIssue><PubDate><MedlineDate>2024 Winter</MedlineDate></PubDate></JournalIssue><Title>Medical Journal</Title></Journal></Article></MedlineCitation><PubmedData><ArticleIdList/></PubmedData></PubmedArticle>
</PubmedArticleSet>`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	client := NewPubMedClient(&PubMedConfig{Tool: "psitech-test", Email: "ops@example.test", APIKey: "secret", BaseURL: upstream.URL, HTTPClient: upstream.Client()})
	client.interval = 0
	result, err := client.Search(context.Background(), "sleep therapy", 0, 2, "relevance")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 2 || result.Items[0].PMID != "20" || result.Items[1].PMID != "10" {
		t.Fatalf("upstream ranking was not preserved: %#v", result.Items)
	}
	if result.Items[0].Abstract != "Full abstract." || result.Items[0].Authors[0] != "Study Group" || result.Items[0].PublishedAt != "2024 Winter" {
		t.Fatalf("first article was not normalized: %#v", result.Items[0])
	}
	if result.Items[1].Abstract != "BACKGROUND: Background text.\n\nResult text." || result.Items[1].DOI != "10.1/example" {
		t.Fatalf("structured abstract missing: %#v", result.Items[1])
	}
	if !result.Truncated || result.NextCursor == nil || result.Total != 10001 {
		t.Fatalf("pagination metadata: %#v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests %d", len(requests))
	}
	for _, query := range requests {
		if query.Get("tool") != "psitech-test" || query.Get("email") != "ops@example.test" || query.Get("api_key") != "secret" || query.Get("db") != "pubmed" {
			t.Fatalf("required NCBI parameters missing: %v", query)
		}
	}
}

func TestPubMedFailuresAndCursor(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"rate", http.StatusTooManyRequests, `{}`, errPubMedRateLimited},
		{"upstream", http.StatusBadGateway, `bad`, errPubMedUnavailable},
		{"invalid json", http.StatusOK, `{`, errPubMedUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer upstream.Close()
			client := NewPubMedClient(&PubMedConfig{Tool: "test", Email: "test@example.test", BaseURL: upstream.URL, HTTPClient: upstream.Client()})
			client.interval = 0
			_, err := client.Search(context.Background(), "query", 0, 10, "relevance")
			if !errorsIs(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
		})
	}
	for _, cursor := range []string{"!", encodePubMedCursor(-1), encodePubMedCursor(10000), base64Cursor("x")} {
		if _, err := decodePubMedCursor(cursor); err == nil {
			t.Fatalf("cursor %q accepted", cursor)
		}
	}
	if offset, err := decodePubMedCursor(encodePubMedCursor(25)); err != nil || offset != 25 {
		t.Fatalf("cursor round trip %d %v", offset, err)
	}
}

func TestPubMedHandlerValidationAndRole(t *testing.T) {
	server := &Server{pubmed: NewPubMedClient(&PubMedConfig{Tool: "test", Email: "test@example.test"})}
	principalDoctor := principal{User: User{ID: "doctor", Role: "doctor"}}
	for _, test := range []struct {
		body string
		want int
	}{
		{`{"query":"x"}`, 400},
		{`{"query":"valid","limit":21}`, 400},
		{`{"query":"valid","sort":"unknown"}`, 400},
		{`{"query":"valid","cursor":"bad!"}`, 400},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/doctor/pubmed/search", strings.NewReader(test.body))
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principalDoctor))
		w := httptest.NewRecorder()
		server.searchPubMed(w, r)
		if w.Code != test.want {
			t.Fatalf("body %s: %d %s", test.body, w.Code, w.Body.String())
		}
	}
	if NewPubMedClient(nil) != nil || NewPubMedClient(&PubMedConfig{Tool: "test"}) != nil {
		t.Fatal("incomplete configuration enabled PubMed")
	}
}

func errorsIs(got, want error) bool {
	return got != nil && want != nil && (got == want || strings.Contains(got.Error(), want.Error()))
}

func base64Cursor(value string) string {
	encoded, _ := json.Marshal(value)
	return strings.Trim(string(encoded), `"`)
}

func TestPubMedLimiterHonorsCancellation(t *testing.T) {
	client := NewPubMedClient(&PubMedConfig{Tool: "test", Email: "test@example.test"})
	client.interval = time.Hour
	if err := client.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.wait(ctx); err == nil {
		t.Fatal("cancelled limiter wait succeeded")
	}
}
