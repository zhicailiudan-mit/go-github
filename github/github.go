// Copyright 2026 The go-github AUTHORS. All rights reserved.
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/go-querystring/query"
)

const (
	defaultBaseURL = "https://api.github.com/"
	mediaTypeJSON  = "application/json"
)

// ListOptions specifies the optional parameters to various List methods that
// support pagination.
type ListOptions struct {
	// Page is the page number of the results to fetch.
	// When Page is 0, the first page is fetched.
	Page int `url:"page,omitempty"`

	// PerPage is the number of results to include per page. If PerPage is 0
	// (the zero value), per_page is omitted from the request query string and
	// the GitHub API default page size (currently 30) applies. In that case
	// the server may still echo an explicit per_page value in Link headers;
	// the pagination helpers on Response (NextPage, PrevPage, FirstPage,
	// LastPage) extract only the page number from those links, so they work
	// identically whether per_page was sent explicitly or defaulted by the
	// server.
	PerPage int `url:"per_page,omitempty"`
}

// Response is a go-github response. This wraps the standard http.Response
// returned from GitHub and provides pagination information parsed from the
// HTTP Link header.
type Response struct {
	*http.Response

	// FirstPage is the page number of the first available page (0 if unknown).
	FirstPage int

	// PrevPage is the page number of the previous available page (0 if unknown).
	PrevPage int

	// NextPage is the page number of the next available page (0 if unknown).
	NextPage int

	// LastPage is the page number of the last available page (0 if unknown).
	LastPage int
}

// newResponse creates a new Response for the provided http.Response and
// parses pagination information out of its Link header.
func newResponse(r *http.Response) *Response {
	response := &Response{Response: r}
	response.populatePageValues()
	return response
}

// populatePageValues parses the HTTP Link header of the response and fills in
// the pagination field values from the "page" query parameter of each link
// relation. Links that omit per_page (either in the request or in the echoed
// URL) are handled the same way as links that carry an explicit per_page,
// because only the page number is extracted.
func (r *Response) populatePageValues() {
	if r.Response == nil || r.Response.Header == nil {
		return
	}

	for _, linkHeader := range r.Response.Header["Link"] {
		for _, link := range strings.Split(linkHeader, ",") {
			segments := strings.Split(link, ";")
			if len(segments) < 2 {
				continue
			}

			linkURL, err := url.Parse(strings.Trim(strings.TrimSpace(segments[0]), "<>"))
			if err != nil {
				continue
			}

			pageStr := linkURL.Query().Get("page")
			if pageStr == "" {
				continue
			}

			page, err := strconv.Atoi(pageStr)
			if err != nil {
				continue
			}

			for _, segment := range segments[1:] {
				key, value, found := strings.Cut(segment, "=")
				if !found || strings.TrimSpace(key) != "rel" {
					continue
				}

				switch strings.Trim(value, `"' `) {
				case "first":
					r.FirstPage = page
				case "prev":
					r.PrevPage = page
				case "next":
					r.NextPage = page
				case "last":
					r.LastPage = page
				}
			}
		}
	}
}

// addOptions adds the parameters in opts as URL query parameters to s. opts
// must be a struct whose fields may contain "url" tags (e.g. ListOptions).
//
// Parameters already present in s are preserved; parameters set in opts take
// precedence only for the keys opts actually carries. Because ListOptions
// fields are tagged omitempty, a zero PerPage neither drops other existing
// query parameters nor introduces a conflicting per_page value — the server
// default page size simply applies.
func addOptions(s string, opts interface{}) (string, error) {
	if opts == nil {
		return s, nil
	}

	v := reflect.ValueOf(opts)
	if v.Kind() == reflect.Ptr && v.IsNil() {
		return s, nil
	}

	u, err := url.Parse(s)
	if err != nil {
		return s, err
	}

	qs, err := query.Values(opts)
	if err != nil {
		return s, err
	}

	q := u.Query()
	for key, values := range qs {
		for _, value := range values {
			q.Set(key, value)
		}
	}
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// Client manages communication with the GitHub API.
type Client struct {
	client *http.Client

	// BaseURL is the base URL for API requests, with a trailing slash.
	BaseURL *url.URL

	// Repositories provides access to the repository-related endpoints.
	Repositories *RepositoriesService
}

// NewClient returns a new GitHub API client. If a nil httpClient is
// provided, http.DefaultClient is used.
func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{}
	}

	baseURL, _ := url.Parse(defaultBaseURL)

	c := &Client{
		client:  httpClient,
		BaseURL: baseURL,
	}
	c.Repositories = &RepositoriesService{client: c}

	return c
}

// NewRequest creates an API request relative to the client's BaseURL.
// urlStr may contain existing query parameters; if opts is non-nil, its
// fields are encoded as additional query parameters (see addOptions).
func (c *Client) NewRequest(method, urlStr string, body io.Reader, opts interface{}) (*http.Request, error) {
	mergedURL, err := addOptions(c.BaseURL.String()+urlStr, opts)
	if err != nil {
		return nil, err
	}

	u, err := url.Parse(mergedURL)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(method, u.String(), body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", mediaTypeJSON)
	req.Header.Set("Content-Type", mediaTypeJSON)

	return req, nil
}

// Do sends an API request and returns the API response. The pagination
// information is populated from the Link header of the response.
func (c *Client) Do(ctx context.Context, req *http.Request) (*Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req = req.WithContext(ctx)

	httpResp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	// Note: the response body is NOT closed here. Callers must consume it
	// (e.g. via decodeJSON in the endpoint methods), which closes it.

	response := newResponse(httpResp)

	if response.StatusCode < 200 || response.StatusCode > 299 {
		httpResp.Body.Close()
		return response, fmt.Errorf("github: request failed with status %d %s", response.StatusCode, http.StatusText(response.StatusCode))
	}

	return response, nil
}

// Repository represents a minimal GitHub repository object returned by list
// endpoints used with ListOptions pagination.
type Repository struct {
	// ID uniquely identifies the repository.
	ID int64

	// Name is the repository name.
	Name string
}

// RepositoriesService provides access to the repository-related endpoints in
// the GitHub API.
type RepositoriesService struct {
	client *Client
}

// List lists the repositories for the authenticated user, supporting
// pagination through ListOptions.
func (s *RepositoriesService) List(ctx context.Context, opts *ListOptions) ([]*Repository, *Response, error) {
	req, err := s.client.NewRequest(http.MethodGet, "user/repos", nil, opts)
	if err != nil {
		return nil, nil, err
	}

	var repositories []*Repository
	resp, err := s.client.Do(ctx, req)
	if err != nil {
		return nil, resp, err
	}

	if err := decodeJSON(resp.Response, &repositories); err != nil {
		return nil, resp, err
	}

	return repositories, resp, nil
}

// decodeJSON decodes the JSON body of r into v.
func decodeJSON(r *http.Response, v interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
