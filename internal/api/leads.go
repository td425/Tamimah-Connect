package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/td425/tpbx/internal/store"
)

// Lead lists ---------------------------------------------------------------

func (s *Server) handleListLists(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	lists, err := s.Lists.List(ctx)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lists": lists})
}

func (s *Server) handleGetList(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	l, err := s.Lists.Get(ctx, id)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (s *Server) handleCreateList(w http.ResponseWriter, r *http.Request) {
	var body store.List
	if !decodeJSON(w, r, &body, 1<<18) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	l, err := s.Lists.Create(ctx, body)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, l)
}

func (s *Server) handleUpdateList(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body store.List
	if !decodeJSON(w, r, &body, 1<<18) {
		return
	}
	body.ID = id
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Lists.Update(ctx, body); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// handleDeleteList removes a list and every lead in it. The client must send
// the lead count it showed the operator (?leads=N); the store refuses if the
// list has grown since, so a delete can never destroy more than was confirmed.
func (s *Server) handleDeleteList(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	expect, err := strconv.Atoi(r.URL.Query().Get("leads"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "confirm the delete by sending the list's lead count as ?leads=N",
		})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.Lists.Delete(ctx, id, expect); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleResetList puts every lead in a list back to NEW with a cleared call
// counter, so the list can be run again from the top.
func (s *Server) handleResetList(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	n, err := s.Lists.ResetLeads(ctx, id)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "reset", "leads": n})
}

// Leads --------------------------------------------------------------------

// handleSearchLeads is the console's lead browser: filter by list, status,
// number, name, owner and date window, with paging.
func (s *Server) handleSearchLeads(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := store.LeadQuery{
		Status:     q.Get("status"),
		Phone:      q.Get("phone"),
		Name:       q.Get("name"),
		Owner:      q.Get("owner"),
		Since:      q.Get("since"),
		Until:      q.Get("until"),
		OrderNewer: q.Get("order") != "oldest",
	}
	if v := q.Get("listId"); v != "" {
		query.ListID, _ = strconv.ParseInt(v, 10, 64)
	}
	if v := q.Get("limit"); v != "" {
		query.Limit, _ = strconv.Atoi(v)
	}
	if v := q.Get("offset"); v != "" {
		query.Offset, _ = strconv.Atoi(v)
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	leads, total, err := s.Leads.Search(ctx, query)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"leads":  leads,
		"total":  total,
		"offset": query.Offset,
	})
}

// handleLeadStatuses returns the dial statuses the system itself sets, for the
// console's filter and picker. A lead may carry any status; campaigns bring
// their own dispositions in phase 2.
func (s *Server) handleLeadStatuses(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"statuses": store.SystemStatuses})
}

func (s *Server) handleGetLead(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	d, err := s.Leads.Get(ctx, id)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleCreateLead(w http.ResponseWriter, r *http.Request) {
	var body struct {
		store.Lead
		DupScope string `json:"dupScope"`
		DupDays  int    `json:"dupDays"`
	}
	if !decodeJSON(w, r, &body, 1<<18) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	d, err := s.Leads.Create(ctx, body.Lead, store.DupScope(body.DupScope), body.DupDays)
	if err != nil {
		writeLeadError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

func (s *Server) handleUpdateLead(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	var body store.Lead
	if !decodeJSON(w, r, &body, 1<<18) {
		return
	}
	body.ID = id
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Leads.Update(ctx, body); err != nil {
		writeLeadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (s *Server) handleDeleteLead(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid id"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.Leads.Delete(ctx, id); err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// handleSetLeadStatus re-qualifies a selection of leads in one request.
func (s *Server) handleSetLeadStatus(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs    []int64 `json:"ids"`
		Status string  `json:"status"`
	}
	if !decodeJSON(w, r, &body, 1<<18) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	n, err := s.Leads.SetStatus(ctx, body.IDs, body.Status)
	if err != nil {
		writeExtError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "updated": n})
}

// handleBulkLeads imports many leads into one list. Each row is attempted
// independently and reported on by index, so one bad row (or one duplicate)
// never costs the rest of the file — the same contract as the extensions bulk
// upload.
func (s *Server) handleBulkLeads(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ListID   int64        `json:"listId"`
		DupScope string       `json:"dupScope"`
		DupDays  int          `json:"dupDays"`
		Leads    []store.Lead `json:"leads"`
	}
	if !decodeJSON(w, r, &body, 8<<20) {
		return
	}
	if len(body.Leads) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no leads supplied"})
		return
	}
	if len(body.Leads) > 5000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "no more than 5000 leads per upload — split the file",
		})
		return
	}

	type rowResult struct {
		Row   int    `json:"row"` // 1-based, matches the source file
		ID    int64  `json:"id,omitempty"`
		Phone string `json:"phone"`
		OK    bool   `json:"ok"`
		Error string `json:"error,omitempty"`
	}
	results := make([]rowResult, 0, len(body.Leads))
	created, duplicates := 0, 0

	for i, lead := range body.Leads {
		if body.ListID > 0 {
			lead.ListID = body.ListID
		}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		d, err := s.Leads.Create(ctx, lead, store.DupScope(body.DupScope), body.DupDays)
		cancel()
		switch {
		case err == nil:
			created++
			results = append(results, rowResult{Row: i + 1, ID: d.ID, Phone: d.PhoneNumber, OK: true})
		case errors.Is(err, store.ErrDuplicate):
			duplicates++
			results = append(results, rowResult{Row: i + 1, Phone: lead.PhoneNumber, Error: err.Error()})
		default:
			results = append(results, rowResult{Row: i + 1, Phone: lead.PhoneNumber, Error: err.Error()})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"created":    created,
		"duplicates": duplicates,
		"failed":     len(body.Leads) - created,
		"results":    results,
	})
}

// Shared helpers -----------------------------------------------------------

// decodeJSON reads a JSON body of at most max bytes, writing the error response
// itself and reporting whether the caller may continue.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, max int64) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, max)).Decode(dst); err != nil {
		msg := "invalid JSON body"
		if strings.Contains(err.Error(), "too large") {
			msg = "request body is too large"
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return false
	}
	return true
}

// writeLeadError maps a duplicate to 409 and otherwise defers to the shared
// not-found/conflict/validation mapping.
func writeLeadError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrDuplicate) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeExtError(w, err)
}
