package models

import (
	"errors"
	"strings"
)

const (
	defaultPerPage = 12
	maxPerPage     = 100
)

// Response is the envelope wrapping every API reply. On success Error is nil;
// on failure Data is nil and Error carries the detail. The frontend api client
// (src/api/client.ts) unwraps `data` and throws on a non-nil `error`.
type Response struct {
	Data  any `json:"data"`
	Error any `json:"error"`
}

// ErrorResponse is the typical shape placed in Response.Error.
type ErrorResponse struct {
	Message string `json:"message"`
	Code    int    `json:"code"`
}

// Page is the payload of page-based list endpoints, carrying the rows for the
// current page plus enough metadata for the client to render a pager without a
// second request: `{ data: { list, total, page, per_page, total_pages } }`.
type Page[T any] struct {
	List       []T   `json:"list"`
	Total      int64 `json:"total"`
	Page       int   `json:"page"`
	PerPage    int   `json:"per_page"`
	TotalPages int64 `json:"total_pages"`
}

// NewPage assembles a Page from a result set, its total, and the query params.
func NewPage[T any](list []T, total int64, p PaginationParams) Page[T] {
	totalPages := int64(0)
	if p.PerPage > 0 {
		totalPages = (total + int64(p.PerPage) - 1) / int64(p.PerPage)
	}
	return Page[T]{
		List:       list,
		Total:      total,
		Page:       p.Page,
		PerPage:    p.PerPage,
		TotalPages: totalPages,
	}
}

// PaginationParams are the query params accepted by list endpoints.
type PaginationParams struct {
	Page    int    `query:"page"`
	PerPage int    `query:"per_page"`
	Search  string `query:"search"`
}

// Normalize applies defaults/bounds and trims the search term. It never fails
// for empty input; it only rejects a too-short (but non-empty) search.
func (p *PaginationParams) Normalize() error {
	p.Search = strings.TrimSpace(p.Search)
	if p.Page <= 0 {
		p.Page = 1
	}
	if p.PerPage <= 0 {
		p.PerPage = defaultPerPage
	}
	if p.PerPage > maxPerPage {
		p.PerPage = maxPerPage
	}
	if p.Search != "" && len(p.Search) < 2 {
		return errors.New("search term too short")
	}
	return nil
}

// Offset is the SQL OFFSET implied by the (1-based) page and per-page size.
func (p PaginationParams) Offset() int {
	if p.Page <= 1 {
		return 0
	}
	return (p.Page - 1) * p.PerPage
}
