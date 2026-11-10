//go:build !remote && (linux || freebsd)

package compat

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNegotiateEventsContentType(t *testing.T) {
	tests := []struct {
		name     string
		accept   string
		expected string
	}{
		{
			name:     "empty accept header defaults to json",
			accept:   "",
			expected: "application/json",
		},
		{
			name:     "explicit application/json",
			accept:   "application/json",
			expected: "application/json",
		},
		{
			name:     "x-ndjson",
			accept:   "application/x-ndjson",
			expected: "application/x-ndjson",
		},
		{
			name:     "json-seq",
			accept:   "application/json-seq",
			expected: "application/json-seq",
		},
		{
			name:     "jsonl",
			accept:   "application/jsonl",
			expected: "application/jsonl",
		},
		{
			name:     "wildcard defaults to json",
			accept:   "*/*",
			expected: "application/json",
		},
		{
			name:     "type wildcard picks server preference",
			accept:   "application/*",
			expected: "application/json",
		},
		{
			name:     "unsupported type falls back to json",
			accept:   "text/html",
			expected: "application/json",
		},
		{
			name:     "malformed entries fall back to json",
			accept:   "not a media type",
			expected: "application/json",
		},
		{
			name:     "higher q wins",
			accept:   "application/json-seq;q=0.5, application/jsonl;q=0.9",
			expected: "application/jsonl",
		},
		{
			name:     "equal q picks server preference order",
			accept:   "application/jsonl, application/json-seq",
			expected: "application/jsonl",
		},
		{
			name:     "specific match beats wildcard regardless of order",
			accept:   "application/*;q=0.9, application/json-seq;q=0.1",
			expected: "application/json",
		},
		{
			name:     "specific match wins when it has higher q",
			accept:   "application/*;q=0.1, application/json-seq;q=0.9",
			expected: "application/json-seq",
		},
		{
			name:     "q=0 excludes a type",
			accept:   "application/json;q=0, application/jsonl",
			expected: "application/jsonl",
		},
		{
			name:     "all q=0 falls back to json",
			accept:   "application/json;q=0, application/jsonl;q=0",
			expected: "application/json",
		},
		{
			name:     "whitespace and parameters tolerated",
			accept:   "  application/jsonl ; q=0.8 , application/x-ndjson",
			expected: "application/x-ndjson",
		},
		{
			name:     "media type parameters ignored for matching",
			accept:   "application/jsonl;charset=utf-8",
			expected: "application/jsonl",
		},
		{
			name:     "multiple supported types picks highest q",
			accept:   "application/x-ndjson;q=0.5, application/json-seq;q=0.7, application/jsonl;q=0.8",
			expected: "application/jsonl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, negotiateEventsContentType(tt.accept))
		})
	}
}
