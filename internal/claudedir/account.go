package claudedir

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// AccountPath is ~/.claude.json — Claude Code's root config, a sibling of the
// .claude directory. It holds the signed-in account profile and a cached copy
// of the plan's usage-limit utilisation. It does NOT hold tokens (those live
// in the deny-listed .claude/.credentials.json), but it is still read through
// a typed decoder so only the fields below ever leave this function.
func (d Dir) AccountPath() string {
	return filepath.Join(filepath.Dir(d.Root), ".claude.json")
}

// Account is the safe subset of ~/.claude.json shown in the UI.
type Account struct {
	Email                 string     `json:"email"`
	OrganizationName      string     `json:"organization_name"`
	OrganizationType      string     `json:"organization_type"` // claude_pro, claude_max, …
	BillingType           string     `json:"billing_type"`
	Role                  string     `json:"role"`
	AccountCreatedAt      string     `json:"account_created_at"`
	SubscriptionCreatedAt string     `json:"subscription_created_at"`
	ExtraUsageEnabled     bool       `json:"extra_usage_enabled"`
	FirstStart            string     `json:"first_start"`
	Startups              int        `json:"startups"`
	InstallMethod         string     `json:"install_method"`
	Usage                 *PlanUsage `json:"usage,omitempty"`
}

// PlanUsage mirrors cachedUsageUtilization: percentage of each rate-limit
// window already used, as last fetched by Claude Code itself.
type PlanUsage struct {
	FetchedAt string      `json:"fetched_at"`
	Windows   []UsageSlot `json:"windows"`
}

// UsageSlot is one rate-limit window.
type UsageSlot struct {
	Key         string `json:"key"`   // five_hour, seven_day, seven_day_opus, …
	Label       string `json:"label"` // "5-hour", "7-day", …
	Utilization int    `json:"utilization"`
	ResetsAt    string `json:"resets_at"`
	Locked      string `json:"locked_reason,omitempty"`
}

// Stale reports whether the window has already reset since the snapshot was
// taken, i.e. the cached utilisation no longer applies.
func (u UsageSlot) Stale() bool {
	t, err := time.Parse(time.RFC3339Nano, u.ResetsAt)
	return err == nil && t.Before(time.Now())
}

// PlanLabel turns an organizationType into a display name.
func PlanLabel(orgType string) string {
	switch strings.ToLower(orgType) {
	case "claude_pro":
		return "Pro"
	case "claude_max":
		return "Max"
	case "claude_team", "team":
		return "Team"
	case "claude_enterprise", "enterprise":
		return "Enterprise"
	case "":
		return ""
	}
	// e.g. "claude_something" → "Something"
	s := strings.TrimPrefix(strings.ToLower(orgType), "claude_")
	s = strings.ReplaceAll(s, "_", " ")
	r := []rune(strings.TrimSpace(s))
	if len(r) == 0 {
		return "Unknown"
	}
	return string(unicode.ToUpper(r[0])) + string(r[1:])
}

var usageWindowLabels = map[string]string{
	"five_hour":        "5-hour window",
	"seven_day":        "7-day window",
	"seven_day_opus":   "7-day Opus",
	"seven_day_sonnet": "7-day Sonnet",
	"seven_day_cowork": "7-day Cowork",
}

// ReadAccount decodes the account profile and usage cache. A missing file
// yields (nil, nil); a file without oauthAccount yields an Account with only
// the install facts filled in.
func (d Dir) ReadAccount() (*Account, error) {
	// Deliberately bypasses Dir.ReadFile: this is the one sanctioned read
	// outside Root, and only the typed subset below ever leaves this function.
	b, err := os.ReadFile(d.AccountPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var raw struct {
		OAuth *struct {
			Email                 string `json:"emailAddress"`
			OrganizationName      string `json:"organizationName"`
			OrganizationType      string `json:"organizationType"`
			BillingType           string `json:"billingType"`
			Role                  string `json:"organizationRole"`
			AccountCreatedAt      string `json:"accountCreatedAt"`
			SubscriptionCreatedAt string `json:"subscriptionCreatedAt"`
			ExtraUsage            bool   `json:"hasExtraUsageEnabled"`
		} `json:"oauthAccount"`
		FirstStart    string `json:"firstStartTime"`
		Startups      int    `json:"numStartups"`
		InstallMethod string `json:"installMethod"`
		Usage         *struct {
			FetchedAtMs int64 `json:"fetchedAtMs"`
			// Values are heterogeneous (objects, nulls, arrays for experimental
			// keys), so decode each known window individually.
			Utilization map[string]json.RawMessage `json:"utilization"`
		} `json:"cachedUsageUtilization"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	type window struct {
		Utilization  float64 `json:"utilization"`
		ResetsAt     string  `json:"resets_at"`
		LockedReason string  `json:"locked_reason"`
	}
	acct := &Account{FirstStart: raw.FirstStart, Startups: raw.Startups, InstallMethod: raw.InstallMethod}
	if raw.OAuth != nil {
		acct.Email = raw.OAuth.Email
		acct.OrganizationName = raw.OAuth.OrganizationName
		acct.OrganizationType = raw.OAuth.OrganizationType
		acct.BillingType = raw.OAuth.BillingType
		acct.Role = raw.OAuth.Role
		acct.AccountCreatedAt = raw.OAuth.AccountCreatedAt
		acct.SubscriptionCreatedAt = raw.OAuth.SubscriptionCreatedAt
		acct.ExtraUsageEnabled = raw.OAuth.ExtraUsage
	}
	if raw.Usage != nil && len(raw.Usage.Utilization) > 0 {
		pu := &PlanUsage{}
		if raw.Usage.FetchedAtMs > 0 {
			pu.FetchedAt = time.UnixMilli(raw.Usage.FetchedAtMs).UTC().Format(time.RFC3339Nano)
		}
		// Fixed order: the two headline windows first, then any model-specific ones.
		for _, key := range []string{"five_hour", "seven_day", "seven_day_opus", "seven_day_sonnet", "seven_day_cowork"} {
			rawWin, ok := raw.Usage.Utilization[key]
			if !ok || len(rawWin) == 0 || string(rawWin) == "null" || rawWin[0] != '{' {
				continue
			}
			var u window
			if json.Unmarshal(rawWin, &u) != nil {
				continue
			}
			pu.Windows = append(pu.Windows, UsageSlot{
				Key: key, Label: usageWindowLabels[key], Utilization: int(u.Utilization + 0.5),
				ResetsAt: u.ResetsAt, Locked: u.LockedReason,
			})
		}
		if len(pu.Windows) > 0 {
			acct.Usage = pu
		}
	}
	return acct, nil
}

// DisplayName is the part of the email before "@", or the org name.
func (a *Account) DisplayName() string {
	if a == nil {
		return ""
	}
	if a.Email != "" {
		if i := strings.Index(a.Email, "@"); i > 0 {
			return a.Email[:i]
		}
		return a.Email
	}
	return a.OrganizationName
}
