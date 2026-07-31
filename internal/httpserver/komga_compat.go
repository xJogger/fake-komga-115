package httpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/xJogger/fake-komga-115/internal/database"
)

const (
	maxClientSettingKeyLength   = 256
	maxClientSettingValueLength = 8192
)

type komgaSearchFilters struct {
	Search       string
	LibraryIDs   []string
	ReadStatuses []string
	OneShot      *bool
	Empty        bool
}

type komgaSearchRequest struct {
	Condition      json.RawMessage `json:"condition"`
	FullTextSearch *string         `json:"fullTextSearch"`
}

func (s *Server) komgaMe(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"id":    "local",
		"email": "local@fake-komga-115",
		"roles": []string{"USER", "ADMIN"},
	})
}

func (s *Server) komgaClientSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.ClientSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	out := make(map[string]map[string]any, len(settings))
	for key, item := range settings {
		out[key] = map[string]any{
			"value":                item.Value,
			"allowUnauthenticated": item.AllowUnauthenticated,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) patchKomgaClientSettings(w http.ResponseWriter, r *http.Request) {
	var request map[string]struct {
		Value                *string `json:"value"`
		AllowUnauthenticated bool    `json:"allowUnauthenticated"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	for key, update := range request {
		if err := validateClientSetting(key, update.Value); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_CLIENT_SETTING", err.Error())
			return
		}
	}
	for key, update := range request {
		var err error
		if update.Value == nil {
			err = s.store.DeleteClientSetting(r.Context(), key)
		} else {
			err = s.store.UpsertClientSetting(
				r.Context(), key, *update.Value, update.AllowUnauthenticated,
			)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func validateClientSetting(key string, value *string) error {
	key = strings.TrimSpace(key)
	switch {
	case key == "":
		return errors.New("client setting key cannot be empty")
	case len(key) > maxClientSettingKeyLength:
		return errors.New("client setting key is too long")
	case !strings.HasPrefix(key, "koharia.") && !strings.HasPrefix(key, "webui."):
		return errors.New("client setting key must start with koharia. or webui.")
	}
	lowerKey := strings.ToLower(key)
	for _, marker := range []string{"token", "authorization", "cookie", "password"} {
		if strings.Contains(lowerKey, marker) {
			return errors.New("client setting key looks sensitive")
		}
	}
	if value != nil && len(*value) > maxClientSettingValueLength {
		return errors.New("client setting value is too long")
	}
	return nil
}

func (s *Server) komgaSeriesList(w http.ResponseWriter, r *http.Request) {
	page, size := intQuery(r, "page", 0), intQuery(r, "size", 20)
	filters, ok := readKomgaSearchRequest(w, r, "series")
	if !ok {
		return
	}
	items, total, err := s.store.SeriesPage(r.Context(), database.SeriesQuery{
		Search: filters.Search, LibraryIDs: filters.LibraryIDs,
		ReadStatus: filters.ReadStatuses, OneShot: filters.OneShot,
		Empty: filters.Empty, Page: page, Size: size, Sort: r.URL.Query().Get("sort"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	out, err := s.seriesDTOs(r, items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, makePage(out, page, size, total, false))
}

func (s *Server) komgaBooksList(w http.ResponseWriter, r *http.Request) {
	page, size := intQuery(r, "page", 0), intQuery(r, "size", 20)
	filters, ok := readKomgaSearchRequest(w, r, "books")
	if !ok {
		return
	}
	items, total, err := s.store.BooksPage(r.Context(), database.BookQuery{
		Search: filters.Search, LibraryIDs: filters.LibraryIDs,
		ReadStatus: filters.ReadStatuses, OneShot: filters.OneShot,
		Empty: filters.Empty, Page: page, Size: size, Sort: r.URL.Query().Get("sort"),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	out, err := s.bookDTOs(r, items)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "DATABASE_ERROR", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, makePage(out, page, size, total, false))
}

func readKomgaSearchRequest(
	w http.ResponseWriter,
	r *http.Request,
	target string,
) (komgaSearchFilters, bool) {
	var request komgaSearchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return komgaSearchFilters{}, false
	}
	filters := komgaSearchFilters{Search: strings.TrimSpace(r.URL.Query().Get("search"))}
	if request.FullTextSearch != nil {
		filters.Search = strings.TrimSpace(*request.FullTextSearch)
	}
	if len(request.Condition) != 0 && string(request.Condition) != "null" {
		if err := applyKomgaCondition(request.Condition, target, &filters); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_SEARCH_CONDITION", err.Error())
			return komgaSearchFilters{}, false
		}
	}
	return filters, true
}

func legacyKomgaFilters(r *http.Request, target string) komgaSearchFilters {
	filters := komgaSearchFilters{
		Search:     strings.TrimSpace(r.URL.Query().Get("search")),
		LibraryIDs: listQuery(r, "library_id"),
		OneShot:    boolQuery(r, "oneshot"),
	}
	statuses, ok := normalizeReadStatuses(listQuery(r, "read_status"))
	if !ok {
		filters.Empty = true
		return filters
	}
	filters.ReadStatuses = statuses
	if queryBoolIsTrueOrInvalid(r, "deleted") {
		filters.Empty = true
		return filters
	}
	if target == "books" && hasNonReadyMediaStatus(r) {
		filters.Empty = true
		return filters
	}
	if target == "series" && !seriesStatusAllowsResults(listQuery(r, "status")) {
		filters.Empty = true
		return filters
	}
	if hasUnsupportedLegacyFilter(r, target) {
		filters.Empty = true
	}
	return filters
}

func applyKomgaCondition(raw json.RawMessage, target string, filters *komgaSearchFilters) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return err
	}
	if allOfRaw, ok := object["allOf"]; ok {
		var children []json.RawMessage
		if err := json.Unmarshal(allOfRaw, &children); err != nil {
			return err
		}
		for _, child := range children {
			if err := applyKomgaCondition(child, target, filters); err != nil {
				return err
			}
		}
		return nil
	}
	if anyOfRaw, ok := object["anyOf"]; ok {
		return applyKomgaAnyOf(anyOfRaw, target, filters)
	}
	return applyKomgaSimpleCondition(object, target, filters)
}

func applyKomgaAnyOf(
	raw json.RawMessage,
	target string,
	filters *komgaSearchFilters,
) error {
	var children []json.RawMessage
	if err := json.Unmarshal(raw, &children); err != nil {
		return err
	}
	for _, child := range children {
		field, value, boolValue, empty, unsupported, err := parseKomgaSimpleCondition(child)
		if err != nil {
			return err
		}
		if unsupported || empty {
			filters.Empty = true
			continue
		}
		applyKomgaField(field, value, boolValue, target, filters)
	}
	return nil
}

func applyKomgaSimpleCondition(
	object map[string]json.RawMessage,
	target string,
	filters *komgaSearchFilters,
) error {
	encoded, err := json.Marshal(object)
	if err != nil {
		return err
	}
	field, value, boolValue, empty, unsupported, err := parseKomgaSimpleCondition(encoded)
	if err != nil {
		return err
	}
	switch {
	case unsupported, empty:
		filters.Empty = true
	default:
		applyKomgaField(field, value, boolValue, target, filters)
	}
	return nil
}

func parseKomgaSimpleCondition(raw json.RawMessage) (
	field string,
	value string,
	boolValue *bool,
	empty bool,
	unsupported bool,
	err error,
) {
	var object map[string]json.RawMessage
	if err = json.Unmarshal(raw, &object); err != nil {
		return "", "", nil, false, false, err
	}
	if len(object) != 1 {
		return "", "", nil, false, true, nil
	}
	for key, rawCondition := range object {
		field = key
		var condition struct {
			Operator json.RawMessage `json:"operator"`
			Value    json.RawMessage `json:"value"`
		}
		if err = json.Unmarshal(rawCondition, &condition); err != nil {
			return "", "", nil, false, false, err
		}
		operator := trimJSONPrimitive(condition.Operator)
		switch strings.ToLower(field) {
		case "deleted":
			switch operator {
			case "isfalse":
				return field, "", boolPtr(false), false, false, nil
			case "istrue":
				return field, "", boolPtr(true), true, false, nil
			}
		case "oneshot":
			switch operator {
			case "isfalse":
				return field, "", boolPtr(false), false, false, nil
			case "istrue":
				return field, "", boolPtr(true), false, false, nil
			}
		case "libraryid", "readstatus", "seriesstatus":
			if err = json.Unmarshal(condition.Value, &value); err != nil {
				return "", "", nil, false, false, err
			}
			return field, value, nil, false, false, nil
		case "genre", "tag", "publisher", "author", "collectionid":
			return field, "", nil, false, true, nil
		default:
			return field, "", nil, false, true, nil
		}
	}
	return "", "", nil, false, true, nil
}

func applyKomgaField(
	field, value string,
	boolValue *bool,
	target string,
	filters *komgaSearchFilters,
) {
	switch strings.ToLower(field) {
	case "deleted":
		if boolValue != nil && *boolValue {
			filters.Empty = true
		}
	case "oneshot":
		filters.OneShot = boolValue
	case "libraryid":
		if value = strings.TrimSpace(value); value != "" {
			filters.LibraryIDs = appendUnique(filters.LibraryIDs, value)
		}
	case "readstatus":
		statuses, ok := normalizeReadStatuses([]string{value})
		if !ok || len(statuses) == 0 {
			filters.Empty = true
			return
		}
		filters.ReadStatuses = appendUnique(filters.ReadStatuses, statuses...)
	case "seriesstatus":
		if target != "series" || strings.ToUpper(strings.TrimSpace(value)) != "ONGOING" {
			filters.Empty = true
		}
	}
}

func trimJSONPrimitive(raw json.RawMessage) string {
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return strings.ToLower(strings.TrimSpace(value))
	}
	return strings.ToLower(strings.Trim(strings.TrimSpace(string(raw)), `"`))
}

func normalizeReadStatuses(values []string) ([]string, bool) {
	if len(values) == 0 {
		return nil, true
	}
	out := make([]string, 0, len(values))
	for _, raw := range values {
		status := strings.ToUpper(strings.TrimSpace(raw))
		if status == "" {
			continue
		}
		switch status {
		case "READ", "IN_PROGRESS", "UNREAD":
			out = appendUnique(out, status)
		default:
			return nil, false
		}
	}
	return out, true
}

func queryBoolIsTrueOrInvalid(r *http.Request, key string) bool {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return false
	}
	parsed, err := parseBoolStrict(value)
	return err != nil || parsed
}

func parseBoolStrict(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, errors.New("invalid boolean")
	}
}

func hasNonReadyMediaStatus(r *http.Request) bool {
	statuses := listQuery(r, "media_status")
	for _, status := range statuses {
		if strings.ToUpper(status) != "READY" {
			return true
		}
	}
	return false
}

func seriesStatusAllowsResults(statuses []string) bool {
	if len(statuses) == 0 {
		return true
	}
	for _, status := range statuses {
		if strings.ToUpper(strings.TrimSpace(status)) == "ONGOING" {
			return true
		}
	}
	return false
}

func hasUnsupportedLegacyFilter(r *http.Request, target string) bool {
	unsupported := []string{"genre", "tag", "publisher", "author", "collection_id"}
	if target == "books" {
		unsupported = []string{"genre", "tag", "publisher", "author", "status", "collection_id"}
	}
	for _, key := range unsupported {
		if len(listQuery(r, key)) > 0 {
			return true
		}
	}
	return false
}

func appendUnique(values []string, additions ...string) []string {
	seen := make(map[string]bool, len(values)+len(additions))
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range additions {
		if value == "" || seen[value] {
			continue
		}
		values = append(values, value)
		seen[value] = true
	}
	return values
}

func boolPtr(value bool) *bool { return &value }
