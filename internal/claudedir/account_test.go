package claudedir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadAccount(t *testing.T) {
	d := fixture(t) // testdata/.claude.json sits beside testdata/claude-home
	a, err := d.ReadAccount()
	if err != nil || a == nil {
		t.Fatalf("account = %+v err=%v", a, err)
	}
	if a.Email != "fixture.user@example.com" || a.OrganizationType != "claude_pro" || a.Role != "admin" ||
		a.BillingType != "stripe_subscription" || a.Startups != 12 || a.InstallMethod != "native" {
		t.Errorf("account = %+v", a)
	}
	if a.DisplayName() != "fixture.user" || PlanLabel(a.OrganizationType) != "Pro" {
		t.Errorf("display %q plan %q", a.DisplayName(), PlanLabel(a.OrganizationType))
	}
	if a.Usage == nil || len(a.Usage.Windows) != 2 {
		t.Fatalf("usage = %+v", a.Usage)
	}
	five, seven := a.Usage.Windows[0], a.Usage.Windows[1]
	if five.Key != "five_hour" || five.Utilization != 29 || five.Stale() {
		t.Errorf("five_hour = %+v stale=%v", five, five.Stale())
	}
	if seven.Key != "seven_day" || seven.Utilization != 4 || !seven.Stale() {
		t.Errorf("seven_day = %+v stale=%v", seven, seven.Stale())
	}
	if a.Usage.FetchedAt != "2026-09-01T10:00:00Z" {
		t.Errorf("fetched_at = %q", a.Usage.FetchedAt)
	}
}

func TestReadAccountMissingAndPartial(t *testing.T) {
	home := t.TempDir()
	d := New(filepath.Join(home, ".claude"))
	if a, err := d.ReadAccount(); err != nil || a != nil {
		t.Errorf("missing file: %+v %v", a, err)
	}
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"numStartups":3}`), 0o644)
	a, err := d.ReadAccount()
	if err != nil || a == nil || a.Email != "" || a.Startups != 3 || a.Usage != nil {
		t.Errorf("partial file: %+v %v", a, err)
	}
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{nope`), 0o644)
	if _, err := d.ReadAccount(); err == nil {
		t.Error("invalid JSON should error")
	}
}

func TestPlanLabel(t *testing.T) {
	for in, want := range map[string]string{
		"claude_pro": "Pro", "claude_max": "Max", "claude_team": "Team", "enterprise": "Enterprise",
		"claude_ultra_new": "Ultra new", "": "", "claude_": "Unknown", "claude_élite": "Élite",
	} {
		if got := PlanLabel(in); got != want {
			t.Errorf("PlanLabel(%q) = %q, want %q", in, got, want)
		}
	}
}
