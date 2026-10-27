package app

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	pubMedDefaultBaseURL = "https://eutils.ncbi.nlm.nih.gov/entrez/eutils"
	pubMedMaxResponse    = 4 << 20
)

var (
	errPubMedRateLimited = errors.New("pubmed rate limited")
	errPubMedUnavailable = errors.New("pubmed unavailable")
)

// PubMedConfig contains the NCBI E-utilities application identity and transport.
type PubMedConfig struct {
	Tool       string
	Email      string
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

// PubMedClient provides bounded access to PubMed through NCBI E-utilities.
type PubMedClient struct {
	tool, email, apiKey, baseURL string
	httpClient                   *http.Client
	interval                     time.Duration
	mu                           sync.Mutex
	next                         time.Time
	cacheMu                      sync.Mutex
	cache                        map[[32]byte]pubMedCacheEntry
}

type pubMedCacheEntry struct {
	result    pubMedResult
	expiresAt time.Time
}

// PubMedArticle is the normalized public representation of one PubMed record.
type PubMedArticle struct {
	PMID        string   `json:"pmid"`
	Title       string   `json:"title"`
	Abstract    string   `json:"abstract"`
	Authors      []string `json:"authors"`
	ArticleTypes []string `json:"article_types"`
	Journal      string   `json:"journal"`
	PublishedAt string   `json:"published_at"`
	DOI         string   `json:"doi,omitempty"`
	URL         string   `json:"url"`
}

type pubMedResult struct {
	Items      []PubMedArticle `json:"items"`
	NextCursor *string         `json:"next_cursor"`
	Total      int             `json:"total"`
	Truncated  bool            `json:"truncated"`
}

// NewPubMedClient validates configuration and constructs a client. Nil means disabled.
func NewPubMedClient(config *PubMedConfig) *PubMedClient {
	if config == nil || strings.TrimSpace(config.Tool) == "" || strings.TrimSpace(config.Email) == "" {
		return nil
	}
	base := strings.TrimRight(config.BaseURL, "/")
	if base == "" {
		base = pubMedDefaultBaseURL
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout:       8 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	interval := time.Second / 3
	if config.APIKey != "" {
		interval = time.Second / 10
	}
	return &PubMedClient{tool: strings.TrimSpace(config.Tool), email: strings.TrimSpace(config.Email), apiKey: strings.TrimSpace(config.APIKey), baseURL: base, httpClient: client, interval: interval, cache: make(map[[32]byte]pubMedCacheEntry)}
}

func (s *Server) searchPubMed(w http.ResponseWriter, r *http.Request) {
	if s.pubmed == nil {
		writeError(w, 503, "Поиск PubMed не настроен")
		return
	}
	var input struct {
		Query  string `json:"query"`
		Cursor string `json:"cursor"`
		Limit  int    `json:"limit"`
		Sort   string `json:"sort"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Query = strings.TrimSpace(input.Query)
	if utf8.RuneCountInString(input.Query) < 2 || utf8.RuneCountInString(input.Query) > 500 {
		writeError(w, 400, "Запрос должен содержать от 2 до 500 символов")
		return
	}
	if input.Limit == 0 {
		input.Limit = 10
	}
	if input.Limit < 1 || input.Limit > 20 {
		writeError(w, 400, "Размер страницы от 1 до 20")
		return
	}
	if input.Sort == "" {
		input.Sort = "relevance"
	}
	if input.Sort != "relevance" && input.Sort != "pub_date" {
		writeError(w, 400, "Доступна сортировка по релевантности или дате")
		return
	}
	offset, err := decodePubMedCursor(input.Cursor)
	if err != nil {
		writeError(w, 400, "Некорректный курсор")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := s.pubmed.Search(ctx, input.Query, offset, input.Limit, input.Sort)
	if err != nil {
		if errors.Is(err, errPubMedRateLimited) {
			writeError(w, 429, "PubMed временно ограничил частоту запросов")
		} else {
			writeError(w, 503, "PubMed временно недоступен")
		}
		return
	}
	if err = s.recordRead(r, "pubmed.search", strconv.Itoa(len(result.Items))); err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, 200, result)
}

// Search returns one normalized PubMed result page including abstracts.
func (c *PubMedClient) Search(ctx context.Context, query string, offset, limit int, sort string) (pubMedResult, error) {
	cacheKey := sha256.Sum256([]byte(query + "\x00" + strconv.Itoa(offset) + "\x00" + strconv.Itoa(limit) + "\x00" + sort))
	if cached, ok := c.cached(cacheKey); ok {
		return cached, nil
	}
	values := c.commonValues()
	values.Set("db", "pubmed")
	values.Set("retmode", "json")
	values.Set("term", query)
	values.Set("retstart", strconv.Itoa(offset))
	values.Set("retmax", strconv.Itoa(limit))
	values.Set("sort", sort)
	var found struct {
		ESearchResult struct {
			Count string   `json:"count"`
			IDs   []string `json:"idlist"`
		} `json:"esearchresult"`
	}
	if err := c.getJSON(ctx, "/esearch.fcgi", values, &found); err != nil {
		return pubMedResult{}, err
	}
	total, err := strconv.Atoi(found.ESearchResult.Count)
	if err != nil || total < 0 {
		return pubMedResult{}, errPubMedUnavailable
	}
	result := pubMedResult{Items: []PubMedArticle{}, Total: total, Truncated: total > 10000}
	if len(found.ESearchResult.IDs) > 0 {
		articles, fetchErr := c.fetch(ctx, found.ESearchResult.IDs)
		if fetchErr != nil {
			return pubMedResult{}, fetchErr
		}
		byID := make(map[string]PubMedArticle, len(articles))
		for _, article := range articles {
			byID[article.PMID] = article
		}
		for _, id := range found.ESearchResult.IDs {
			if article, ok := byID[id]; ok {
				result.Items = append(result.Items, article)
			}
		}
	}
	ceiling := total
	if ceiling > 10000 {
		ceiling = 10000
	}
	if next := offset + len(found.ESearchResult.IDs); next < ceiling {
		cursor := encodePubMedCursor(next)
		result.NextCursor = &cursor
	}
	c.storeCache(cacheKey, result)
	return result, nil
}

func (c *PubMedClient) cached(key [32]byte) (pubMedResult, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	entry, ok := c.cache[key]
	if !ok || time.Now().After(entry.expiresAt) {
		delete(c.cache, key)
		return pubMedResult{}, false
	}
	return entry.result, true
}

func (c *PubMedClient) storeCache(key [32]byte, result pubMedResult) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	now := time.Now()
	for existing, entry := range c.cache {
		if now.After(entry.expiresAt) {
			delete(c.cache, existing)
		}
	}
	if len(c.cache) >= 128 {
		for existing := range c.cache {
			delete(c.cache, existing)
			break
		}
	}
	c.cache[key] = pubMedCacheEntry{result: result, expiresAt: now.Add(5 * time.Minute)}
}

func (c *PubMedClient) fetch(ctx context.Context, ids []string) ([]PubMedArticle, error) {
	values := c.commonValues()
	values.Set("db", "pubmed")
	values.Set("retmode", "xml")
	values.Set("id", strings.Join(ids, ","))
	var set pubMedArticleSet
	if err := c.getXML(ctx, "/efetch.fcgi", values, &set); err != nil {
		return nil, err
	}
	articles := make([]PubMedArticle, 0, len(set.Articles))
	for _, source := range set.Articles {
		pmid := strings.TrimSpace(source.Citation.PMID)
		if _, err := strconv.ParseUint(pmid, 10, 64); err != nil {
			continue
		}
		authors := make([]string, 0, len(source.Citation.Article.Authors))
		for _, author := range source.Citation.Article.Authors {
			name := strings.TrimSpace(strings.TrimSpace(author.ForeName) + " " + strings.TrimSpace(author.LastName))
			if name == "" {
				name = strings.TrimSpace(author.CollectiveName)
			}
			if name != "" {
				authors = append(authors, name)
			}
		}
		parts := make([]string, 0, len(source.Citation.Article.Abstract))
		for _, part := range source.Citation.Article.Abstract {
			value := strings.TrimSpace(string(part.Text))
			if value != "" && part.Label != "" {
				value = strings.TrimSpace(part.Label) + ": " + value
			}
			if value != "" {
				parts = append(parts, value)
			}
		}
		doi := ""
		for _, id := range source.Data.ArticleIDs {
			if strings.EqualFold(id.Type, "doi") {
				doi = strings.TrimSpace(id.Value)
			}
		}
		published := strings.TrimSpace(source.Citation.Article.Journal.Issue.PubDate.Year)
		if published == "" {
			published = strings.TrimSpace(source.Citation.Article.Journal.Issue.PubDate.MedlineDate)
		}
		articles = append(articles, PubMedArticle{
			PMID: pmid, Title: strings.TrimSpace(string(source.Citation.Article.Title)), Abstract: strings.Join(parts, "\n\n"),
			Authors: authors, ArticleTypes: source.Citation.Article.PublicationTypes, Journal: strings.TrimSpace(source.Citation.Article.Journal.Title), PublishedAt: published, DOI: doi,
			URL: "https://pubmed.ncbi.nlm.nih.gov/" + pmid + "/",
		})
	}
	return articles, nil
}

func (c *PubMedClient) commonValues() url.Values {
	values := url.Values{"tool": {c.tool}, "email": {c.email}}
	if c.apiKey != "" {
		values.Set("api_key", c.apiKey)
	}
	return values
}

func (c *PubMedClient) wait(ctx context.Context) error {
	c.mu.Lock()
	now := time.Now()
	reserved := now
	if c.next.After(now) {
		reserved = c.next
	}
	c.next = reserved.Add(c.interval)
	c.mu.Unlock()
	timer := time.NewTimer(time.Until(reserved))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *PubMedClient) request(ctx context.Context, path string, values url.Values) (io.ReadCloser, error) {
	if err := c.wait(ctx); err != nil {
		return nil, fmt.Errorf("pubmed wait: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+values.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("pubmed request: %w", err)
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: request failed", errPubMedUnavailable)
	}
	if response.StatusCode == http.StatusTooManyRequests {
		response.Body.Close()
		return nil, errPubMedRateLimited
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, errPubMedUnavailable
	}
	return response.Body, nil
}

func (c *PubMedClient) getJSON(ctx context.Context, path string, values url.Values, target any) error {
	body, err := c.request(ctx, path, values)
	if err != nil {
		return err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, pubMedMaxResponse+1))
	if err != nil || len(raw) > pubMedMaxResponse {
		return errPubMedUnavailable
	}
	if err = json.Unmarshal(raw, target); err != nil {
		return errPubMedUnavailable
	}
	return nil
}

func (c *PubMedClient) getXML(ctx context.Context, path string, values url.Values, target any) error {
	body, err := c.request(ctx, path, values)
	if err != nil {
		return err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, pubMedMaxResponse+1))
	if err != nil || len(raw) > pubMedMaxResponse {
		return errPubMedUnavailable
	}
	if err = xml.Unmarshal(raw, target); err != nil {
		return errPubMedUnavailable
	}
	return nil
}

func encodePubMedCursor(offset int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func decodePubMedCursor(cursor string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, err
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil || offset < 0 || offset >= 10000 {
		return 0, errors.New("invalid cursor")
	}
	return offset, nil
}

type xmlText string

func (text *xmlText) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	var builder strings.Builder
	for {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		switch value := token.(type) {
		case xml.CharData:
			builder.Write(value)
		case xml.EndElement:
			if value.Name == start.Name {
				*text = xmlText(builder.String())
				return nil
			}
		}
	}
}

type pubMedAbstractText struct {
	Label string
	Text  xmlText
}

func (abstract *pubMedAbstractText) UnmarshalXML(decoder *xml.Decoder, start xml.StartElement) error {
	for _, attribute := range start.Attr {
		if attribute.Name.Local == "Label" {
			abstract.Label = attribute.Value
		}
	}
	return decoder.DecodeElement(&abstract.Text, &start)
}

type pubMedArticleSet struct {
	Articles []struct {
		Citation struct {
			PMID    string `xml:"PMID"`
			Article struct {
				Title    xmlText `xml:"ArticleTitle"`
				Abstract []pubMedAbstractText `xml:"Abstract>AbstractText"`
				Authors []struct {
					LastName       string `xml:"LastName"`
					ForeName       string `xml:"ForeName"`
					CollectiveName string `xml:"CollectiveName"`
				} `xml:"AuthorList>Author"`
				PublicationTypes []string `xml:"PublicationTypeList>PublicationType"`
				Journal struct {
					Title string `xml:"Title"`
					Issue struct {
						PubDate struct {
							Year        string `xml:"Year"`
							MedlineDate string `xml:"MedlineDate"`
						} `xml:"PubDate"`
					} `xml:"JournalIssue"`
				} `xml:"Journal"`
			} `xml:"Article"`
		} `xml:"MedlineCitation"`
		Data struct {
			ArticleIDs []struct {
				Type  string `xml:"IdType,attr"`
				Value string `xml:",chardata"`
			} `xml:"ArticleIdList>ArticleId"`
		} `xml:"PubmedData"`
	} `xml:"PubmedArticle"`
}
