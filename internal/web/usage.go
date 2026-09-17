package web

import (
	"context"
	"net/http"
	"time"

	"claudepulse/internal/claudedir"
)

// usageWindow is one rate-limit window prepared for display: Claude Code's
// last reported utilisation plus what we counted ourselves inside the window.
type usageWindow struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Utilization int    `json:"utilization"`  // % as last reported by Claude Code
	ResetsAt    string `json:"resets_at"`    // RFC3339
	ResetsIn    int64  `json:"resets_in_ms"` // negative when already reset
	Stale       bool   `json:"stale"`        // window reset since the snapshot
	Locked      string `json:"locked_reason,omitempty"`
	Hours       int    `json:"window_hours"`
	// Activity from our own index since the window started (or since the
	// reset, when stale) — a live signal even when Claude Code's snapshot lags.
	Replies      int64  `json:"replies"`
	OutputTokens int64  `json:"output_tokens"`
	Level        string `json:"level"` // ok | warn | danger | stale
}

type usageView struct {
	Plan      string        `json:"plan"`
	FetchedAt string        `json:"fetched_at"`
	Windows   []usageWindow `json:"windows"`
}

var windowHours = map[string]int{"five_hour": 5, "seven_day": 7 * 24, "seven_day_opus": 7 * 24, "seven_day_sonnet": 7 * 24, "seven_day_cowork": 7 * 24}

// buildUsage combines the cached utilisation with trailing activity counts.
func (s *Server) buildUsage(ctx context.Context, acct *claudedir.Account) *usageView {
	if acct == nil || acct.Usage == nil {
		return nil
	}
	now := time.Now()
	v := &usageView{Plan: claudedir.PlanLabel(acct.OrganizationType), FetchedAt: acct.Usage.FetchedAt}
	for _, w := range acct.Usage.Windows {
		uw := usageWindow{Key: w.Key, Label: w.Label, Utilization: w.Utilization, ResetsAt: w.ResetsAt, Locked: w.Locked, Hours: windowHours[w.Key]}
		reset, err := time.Parse(time.RFC3339Nano, w.ResetsAt)
		if err == nil {
			uw.ResetsIn = reset.Sub(now).Milliseconds()
			uw.Stale = reset.Before(now)
		}
		// Window start: reset time minus the window length; if the window has
		// already reset, count from the reset instead (the current window).
		var since time.Time
		if err == nil && uw.Hours > 0 {
			since = reset.Add(-time.Duration(uw.Hours) * time.Hour)
			if uw.Stale {
				since = reset
			}
		} else if uw.Hours > 0 {
			since = now.Add(-time.Duration(uw.Hours) * time.Hour)
		}
		if !since.IsZero() {
			if a, err := s.st.ActivitySince(ctx, since); err == nil {
				uw.Replies, uw.OutputTokens = a.Replies, a.OutputTokens
			}
		}
		switch {
		case uw.Stale:
			uw.Level = "stale"
		case uw.Utilization >= 90:
			uw.Level = "danger"
		case uw.Utilization >= 70:
			uw.Level = "warn"
		default:
			uw.Level = "ok"
		}
		v.Windows = append(v.Windows, uw)
	}
	return v
}

func (s *Server) handleUsageJSON(w http.ResponseWriter, r *http.Request) {
	v := s.buildUsage(r.Context(), s.account(r.Context()))
	if v == nil {
		s.writeJSON(w, http.StatusOK, map[string]any{"available": false,
			"reason": "no usage snapshot in ~/.claude.json yet; start Claude Code once"})
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"available": true, "usage": v})
}

func (s *Server) handleUsagePartial(w http.ResponseWriter, r *http.Request) {
	s.renderPartial(w, "usage.html", s.buildUsage(r.Context(), s.account(r.Context())))
}
