// Copyright 2026 The go-github AUTHORS. All rights reserved.
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPopulatePageValues(t *testing.T) {
	tests := []struct {
		name  string
		link  string
		first int
		prev  int
		next  int
		last  int
	}{
		{
			name: "link header without per_page parameter",
			link: `<https://api.github.com/user/repos?page=1>; rel="first", ` +
				`<https://api.github.com/user/repos?page=2>; rel="next", ` +
				`<https://api.github.com/user/repos?page=5>; rel="last"`,
			first: 1,
			next:  2,
			last:  5,
		},
		{
			name: "link header with explicit per_page parameter",
			link: `<https://api.github.com/user/repos?per_page=50&page=1>; rel="prev", ` +
				`<https://api.github.com/user/repos?per_page=50&page=3>; rel="next", ` +
				`<https://api.github.com/user/repos?per_page=50&page=6>; rel="last"`,
			prev: 1,
			next: 3,
			last: 6,
		},
		{
			name:  "empty link header",
			link:  "",
			first: 0,
			prev:  0,
			next:  0,
			last:  0,
		},
		{
			name:  "malformed link segments are ignored",
			link:  `<https://api.github.com/user/repos?page=2>, junk; rel="next"`,
			first: 0,
			prev:  0,
			next:  0,
			last:  0,
		},
		{
			name:  "non-numeric page parameter is ignored",
			link:  `<https://api.github.com/user/repos?page=abc>; rel="next"`,
			first: 0,
			prev:  0,
			next:  0,
			last:  0,
		},
		{
			name:  "link without page parameter is ignored",
			link:  `<https://api.github.com/user/repos?per_page=30>; rel="next"`,
			first: 0,
			prev:  0,
			next:  0,
			last:  0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			httpResp := &http.Response{
				Header: make(http.Header),
			}
			if tc.link != "" {
				httpResp.Header.Set("Link", tc.link)
			}

			r := newResponse(httpResp)
			if r.FirstPage != tc.first {
				t.Errorf("FirstPage = %d, want %d", r.FirstPage, tc.first)
			}
			if r.PrevPage != tc.prev {
				t.Errorf("PrevPage = %d, want %d", r.PrevPage, tc.prev)
			}
			if r.NextPage != tc.next {
				t.Errorf("NextPage = %d, want %d", r.NextPage, tc.next)
			}
			if r.LastPage != tc.last {
				t.Errorf("LastPage = %d, want %d", r.LastPage, tc.last)
			}
		})
	}
}

func TestAddOptions(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		opts    interface{}
		want    string
	}{
		{
			name:    "PerPage 0 omits per_page but keeps page",
			baseURL: "https://api.github.com/user/repos",
			opts:    &ListOptions{Page: 1, PerPage: 0},
			want:    "https://api.github.com/user/repos?page=1",
		},
		{
			name:    "explicit PerPage is included",
			baseURL: "https://api.github.com/user/repos",
			opts:    &ListOptions{Page: 2, PerPage: 50},
			want:    "https://api.github.com/user/repos?page=2&per_page=50",
		},
		{
			name:    "zero ListOptions adds no parameters",
			baseURL: "https://api.github.com/user/repos",
			opts:    &ListOptions{},
			want:    "https://api.github.com/user/repos",
		},
		{
			name:    "nil opts leaves URL unchanged",
			baseURL: "https://api.github.com/user/repos",
			opts:    nil,
			want:    "https://api.github.com/user/repos",
		},
		{
			name:    "nil pointer opts leaves URL unchanged",
			baseURL: "https://api.github.com/user/repos",
			opts:    (*ListOptions)(nil),
			want:    "https://api.github.com/user/repos",
		},
		{
			name:    "existing query parameters are preserved when PerPage is 0",
			baseURL: "https://api.github.com/user/repos?sort=updated&direction=desc",
			opts:    &ListOptions{Page: 3, PerPage: 0},
			want:    "https://api.github.com/user/repos?direction=desc&page=3&sort=updated",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := addOptions(tc.baseURL, tc.opts)
			if err != nil {
				t.Fatalf("addOptions(%q) returned error: %v", tc.baseURL, err)
			}
			if got != tc.want {
				t.Errorf("addOptions(%q) = %q, want %q", tc.baseURL, got, tc.want)
			}
		})
	}
}

// mockServer builds an httptest server that serves totalPages pages of
// repositories, echoing Link headers with an explicit per_page value (as the
// GitHub API does even when the request omitted per_page).
func mockServer(t *testing.T, totalPages int) (*httptest.Server, *[]string) {
	t.Helper()

	var receivedQueries []string
	mux := http.NewServeMux()
	mux.HandleFunc("/user/repos", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		receivedQueries = append(receivedQueries, r.URL.RawQuery)

		page := 1
		if v := q.Get("page"); v != "" {
			if _, err := fmt.Sscanf(v, "%d", &page); err != nil {
				page = 1
			}
		}
		if page > totalPages {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// The GitHub API echoes the effective per_page in Link headers even
		// when the client omitted per_page (server default of 30 applies).
		effectivePerPage := q.Get("per_page")
		if effectivePerPage == "" {
			effectivePerPage = "30"
		}

		var links []string
		base := "http://" + r.Host + "/user/repos"
		if page > 1 {
			links = append(links, fmt.Sprintf(`<%s?page=%d&per_page=%s>; rel="first"`, base, 1, effectivePerPage))
			links = append(links, fmt.Sprintf(`<%s?page=%d&per_page=%s>; rel="prev"`, base, page-1, effectivePerPage))
		}
		if page < totalPages {
			links = append(links, fmt.Sprintf(`<%s?page=%d&per_page=%s>; rel="next"`, base, page+1, effectivePerPage))
		}
		links = append(links, fmt.Sprintf(`<%s?page=%d&per_page=%s>; rel="last"`, base, totalPages, effectivePerPage))
		w.Header().Set("Link", strings.Join(links, ", "))

		fmt.Fprintf(w, `[{"id": %d, "name": "repo-page-%d"}]`, page, page)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return server, &receivedQueries
}

func newTestClient(t *testing.T, serverURL string) *Client {
	t.Helper()

	client := NewClient(nil)
	u, err := url.Parse(serverURL + "/")
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	client.BaseURL = u

	return client
}

func TestListPerPageZeroFollowsPages(t *testing.T) {
	server, queries := mockServer(t, 3)
	client := newTestClient(t, server.URL)
	ctx := context.Background()

	var names []string
	opts := &ListOptions{PerPage: 0} // rely on the GitHub API default page size
	for {
		repos, resp, err := client.Repositories.List(ctx, opts)
		if err != nil {
			t.Fatalf("List with %v: %v", opts, err)
		}
		for _, repo := range repos {
			names = append(names, repo.Name)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
		opts.PerPage = 0 // keep relying on the server default across pages
	}

	wantNames := []string{"repo-page-1", "repo-page-2", "repo-page-3"}
	if len(names) != len(wantNames) {
		t.Fatalf("collected %d names, want %d: %v", len(names), len(wantNames), names)
	}
	for i, name := range names {
		if name != wantNames[i] {
			t.Errorf("names[%d] = %q, want %q", i, name, wantNames[i])
		}
	}

	// Every request must omit per_page when PerPage is 0, and must still
	// carry the right page number. The first request omits page too
	// (Page: 0 means the first page).
	wantQueries := []string{"", "page=2", "page=3"}
	if len(*queries) != len(wantQueries) {
		t.Fatalf("server received %d requests, want %d: %v", len(*queries), len(wantQueries), *queries)
	}
	for i, q := range *queries {
		if q != wantQueries[i] {
			t.Errorf("request %d query = %q, want %q", i, q, wantQueries[i])
		}
		if strings.Contains(q, "per_page=") {
			t.Errorf("request %d query %q unexpectedly contains per_page", i, q)
		}
	}
}

func TestListExplicitPerPageRetainedAcrossPages(t *testing.T) {
	server, queries := mockServer(t, 2)
	client := newTestClient(t, server.URL)
	ctx := context.Background()

	opts := &ListOptions{Page: 1, PerPage: 50}
	for {
		_, resp, err := client.Repositories.List(ctx, opts)
		if err != nil {
			t.Fatalf("List with %v: %v", opts, err)
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	// Explicit per_page must be retained when moving to the next page.
	for i, q := range *queries {
		if !strings.Contains(q, "per_page=50") {
			t.Errorf("request %d query %q lost per_page=50", i, q)
		}
	}
}
