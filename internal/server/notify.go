package server

import (
	"context"
	"strconv"
	"time"

	"github.com/abjelosevic88/pocket-homelab-requests/internal/notify"
	"github.com/abjelosevic88/pocket-homelab-requests/internal/store"
)

// ---- notifications ----
//
// Each person chooses where theirs go (Settings → Notifications) and which
// moments count: approved / declined, ready, needs a look. Admins also hear
// about requests waiting for them, reported problems and library care.

func wants(st store.Settings, key string) bool {
	switch key {
	case "notify_approved":
		return st.NotifyApproved
	case "notify_available":
		return st.NotifyAvailable
	case "notify_failed":
		return st.NotifyFailed
	case "notify_new_requests":
		return st.NotifyNewRequests
	case "notify_problems":
		return st.NotifyProblems
	case "notify_care":
		return st.NotifyCare
	}
	return true
}

func (s *Server) tellPerson(ctx context.Context, uid int64, title, body, tag, key string) {
	s.tellPersonLink(ctx, uid, title, body, tag, key, "")
}

func (s *Server) tellPersonLink(ctx context.Context, uid int64, title, body, tag, key, link string) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	st, err := s.store.GetSettings(ctx, uid)
	if err != nil || st.NotifyURL == "" || !wants(st, key) {
		return
	}
	prio := "info"
	switch tag {
	case "warning", "rotating_light":
		prio = "warning"
	case "tada", "white_check_mark":
		prio = "success"
	}
	n := notify.New(st.NotifyURL, st.NotifyFormat)
	if err := n.Send(ctx, notify.Message{Title: title, Body: body, Priority: prio, Tags: []string{tag}, Link: link}); err != nil {
		s.log.Debug("notify", "user", uid, "err", err)
	}
}

// tellAdmins sends to every admin who wants this kind of message.
func (s *Server) tellAdmins(ctx context.Context, title, body, tag, key, link string) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return
	}
	for _, u := range users {
		if u.Role == "admin" {
			s.tellPersonLink(ctx, u.ID, title, body, tag, key, link)
		}
	}
}

func (s *Server) tellAdminsPending(ctx context.Context, rq store.Request, who string) {
	link := ""
	if s.cfg.BaseURL != "" {
		link = s.cfg.BaseURL + "/approvals"
	}
	what := map[string]string{"movie": "a film", "series": "a show", "album": "an album"}[rq.Kind]
	body := who + " asked for " + what + ": " + rq.Title
	if rq.Year > 0 {
		body += " (" + strconv.Itoa(rq.Year) + ")"
	}
	if rq.Note != "" {
		body += " — “" + rq.Note + "”"
	}
	s.tellAdmins(ctx, "Waiting for you: "+rq.Title, body, "inbox_tray", "notify_new_requests", link)
}

func (s *Server) tellAdminsProblem(ctx context.Context, rq store.Request, p store.Problem, who string) {
	link := ""
	if s.cfg.BaseURL != "" {
		link = s.cfg.BaseURL + "/problems"
	}
	body := who + ": " + problemKinds[p.Kind]
	if p.Message != "" {
		body += " — “" + p.Message + "”"
	}
	s.tellAdmins(ctx, "Problem with "+rq.Title, body, "warning", "notify_problems", link)
}

func (s *Server) tellAdminsCare(ctx context.Context, title, body string) {
	link := ""
	if s.cfg.BaseURL != "" {
		link = s.cfg.BaseURL + "/care"
	}
	s.tellAdmins(ctx, title, body, "wrench", "notify_care", link)
}
