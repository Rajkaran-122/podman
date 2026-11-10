//go:build !remote && (linux || freebsd)

package compat

import (
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"

	jsoniter "github.com/json-iterator/go"
	"github.com/sirupsen/logrus"
	"go.podman.io/podman/v6/libpod"
	"go.podman.io/podman/v6/libpod/events"
	"go.podman.io/podman/v6/pkg/api/handlers/utils"
	"go.podman.io/podman/v6/pkg/api/handlers/utils/apiutil"
	api "go.podman.io/podman/v6/pkg/api/types"
	"go.podman.io/podman/v6/pkg/domain/entities"
	"go.podman.io/podman/v6/pkg/util"
)

// eventsMediaTypes lists the media types supported for the Docker-compatible
// events stream, in server preference order for equal-quality matches.
var eventsMediaTypes = []string{
	"application/json",
	"application/jsonl",
	"application/x-ndjson",
	"application/json-seq",
}

// eventsJSONSeqRecordSeparator is the RFC 7464 record separator that prefixes
// every record of an application/json-seq stream.
const eventsJSONSeqRecordSeparator = '\x1e'

// acceptSpec is a single parsed media range from an Accept header.
type acceptSpec struct {
	mediaType   string
	q           float64
	specificity int // 2 = exact type, 1 = type/*, 0 = */*
}

// parseAcceptHeader parses an Accept header into media ranges.
// Entries that cannot be parsed are skipped.
func parseAcceptHeader(header string) []acceptSpec {
	var specs []acceptSpec
	for _, part := range strings.Split(header, ",") {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if qs, ok := params["q"]; ok {
			if v, err := strconv.ParseFloat(qs, 64); err == nil {
				q = v
			}
		}
		specificity := 0
		switch {
		case mediaType == "*/*":
			specificity = 0
		case strings.HasSuffix(mediaType, "/*"):
			specificity = 1
		default:
			specificity = 2
		}
		specs = append(specs, acceptSpec{mediaType: mediaType, q: q, specificity: specificity})
	}
	return specs
}

// mediaRangeMatches reports whether an Accept media range covers the given media type.
func mediaRangeMatches(mediaRange, mediaType string) bool {
	switch {
	case mediaRange == "*/*" || mediaRange == mediaType:
		return true
	case strings.HasSuffix(mediaRange, "/*"):
		return strings.HasPrefix(mediaType, strings.TrimSuffix(mediaRange, "*"))
	default:
		return false
	}
}

// negotiateEventsContentType selects the response media type for the
// Docker-compatible events stream based on the request's Accept header.
// It returns "application/json" when no Accept header is present, when nothing
// in the header is supported, or when all matching ranges have q=0, preserving
// the historical default behavior.
func negotiateEventsContentType(accept string) string {
	if accept == "" {
		return "application/json"
	}
	specs := parseAcceptHeader(accept)
	if len(specs) == 0 {
		return "application/json"
	}

	bestType := ""
	bestQ := 0.0
	for _, supported := range eventsMediaTypes {
		// Per RFC 7231, the most specific matching media range determines
		// the quality for a given media type.
		q := -1.0
		specificity := -1
		for _, spec := range specs {
			if !mediaRangeMatches(spec.mediaType, supported) {
				continue
			}
			if spec.specificity > specificity || (spec.specificity == specificity && spec.q > q) {
				q = spec.q
				specificity = spec.specificity
			}
		}
		if q <= 0 {
			continue
		}
		if q > bestQ {
			bestQ = q
			bestType = supported
		}
	}
	if bestType == "" {
		return "application/json"
	}
	return bestType
}

// GetEvents endpoint serves both the docker-compatible one and the new libpod one
func GetEvents(w http.ResponseWriter, r *http.Request) {
	var (
		fromStart bool
		decoder   = utils.GetDecoder(r)
		runtime   = r.Context().Value(api.RuntimeKey).(*libpod.Runtime)
		json      = jsoniter.ConfigCompatibleWithStandardLibrary // FIXME: this should happen on the package level
	)

	// NOTE: the "filters" parameter is extracted separately for backwards
	// compat via `filterFromRequest()`.
	query := struct {
		Since  string `schema:"since"`
		Until  string `schema:"until"`
		Stream bool   `schema:"stream"`
	}{
		Stream: true,
	}
	if err := decoder.Decode(&query, r.URL.Query()); err != nil {
		utils.Error(w, http.StatusBadRequest, fmt.Errorf("failed to parse parameters for %s: %w", r.URL.String(), err))
		return
	}

	if len(query.Since) > 0 || len(query.Until) > 0 {
		fromStart = true
	}

	libpodFilters, err := util.FiltersFromRequest(r)
	if err != nil {
		utils.Error(w, http.StatusBadRequest, fmt.Errorf("failed to parse filters for %s: %w", r.URL.String(), err))
		return
	}
	eventChannel := make(chan events.ReadResult)

	readOpts := events.ReadOptions{
		FromStart:    fromStart,
		Stream:       query.Stream,
		Filters:      libpodFilters,
		EventChannel: eventChannel,
		Since:        query.Since,
		Until:        query.Until,
	}
	err = runtime.Events(r.Context(), readOpts)
	if err != nil {
		utils.InternalServerError(w, err)
		return
	}

	flush := func() {}
	if flusher, ok := w.(http.Flusher); ok {
		flush = flusher.Flush
	}

	// Docker-compatible requests negotiate the event stream media type via
	// the Accept header (API v1.52+ for x-ndjson/json-seq, v1.53 for jsonl).
	// Libpod requests keep the historical application/json response.
	contentType := "application/json"
	if !utils.IsLibpodRequest(r) {
		contentType = negotiateEventsContentType(r.Header.Get("Accept"))
	}
	jsonSeq := contentType == "application/json-seq"

	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	flush()

	coder := json.NewEncoder(w)
	coder.SetEscapeHTML(true)

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-eventChannel:
			if !ok {
				return
			}
			if evt.Error != nil {
				logrus.Errorf("Unable to read event: %q", evt.Error)
				continue
			}
			if evt.Event == nil {
				continue
			}

			e := entities.ConvertToEntitiesEvent(*evt.Event)
			// Some events differ between Libpod and Docker endpoints.
			// Handle these differences for Docker-compat.
			if !utils.IsLibpodRequest(r) && e.Type == "image" && e.Action == "remove" {
				// Status is deprecated, but we still like to set it for consumers that might use it.
				e.Status = "delete" //nolint:staticcheck // deprecated field
				e.Action = "delete"
			}
			if !utils.IsLibpodRequest(r) && e.Action == "died" {
				e.Status = "die" //nolint:staticcheck // deprecated field
				e.Action = "die"
				e.Actor.Attributes["exitCode"] = e.Actor.Attributes["containerExitCode"]
			}

			// Remove fields which are not set in 1.52 and newer.
			if _, err := apiutil.SupportedVersion(r, ">=1.52.0"); err == nil && !apiutil.IsLibpodRequest(r) {
				e.Status = "" //nolint:staticcheck // deprecated field, cleared for API >= 1.52
				e.ID = ""     //nolint:staticcheck // deprecated field, cleared for API >= 1.52
				e.From = ""   //nolint:staticcheck // deprecated field, cleared for API >= 1.52
			}

			if jsonSeq {
				// RFC 7464 JSON text sequences prefix every record with RS.
				if _, err := w.Write([]byte{eventsJSONSeqRecordSeparator}); err != nil {
					logrus.Errorf("Unable to write json-seq record separator: %q", err)
					continue
				}
			}
			if err := coder.Encode(e); err != nil {
				logrus.Errorf("Unable to write json: %q", err)
			}
			flush()
		}
	}
}
