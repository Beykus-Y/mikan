package infraalerts

import "testing"

func TestTrackerHysteresis(t *testing.T) {
	var tr Tracker
	if got := tr.Observe(Healthy, 3, 2); got != nil || tr.Level != Healthy {
		t.Fatalf("initial good sample: transition=%v level=%v", got, tr.Level)
	}
	for i := 0; i < 2; i++ {
		if got := tr.Observe(Unavailable, 3, 2); got != nil {
			t.Fatalf("early failure transition: %+v", got)
		}
	}
	if got := tr.Observe(Unavailable, 3, 2); got == nil || got.From != Healthy || got.To != Unavailable {
		t.Fatalf("failure transition: %+v", got)
	}
	if got := tr.Observe(Healthy, 3, 2); got != nil {
		t.Fatalf("early recovery transition: %+v", got)
	}
	if got := tr.Observe(Healthy, 3, 2); got == nil || got.From != Unavailable || got.To != Healthy {
		t.Fatalf("recovery transition: %+v", got)
	}
}

func TestUnknownDoesNotChangeState(t *testing.T) {
	tr := Tracker{Level: Degraded}
	if got := tr.Observe(Unknown, 3, 2); got != nil || tr.Level != Degraded {
		t.Fatalf("unknown sample changed tracker: %+v %+v", got, tr)
	}
}

func TestConfigValidatesPublicTarget(t *testing.T) {
	for _, tc := range []struct {
		channel string
		valid   bool
	}{{"@status_channel", true}, {"-1001234567890", true}, {"", false}, {"https://example.com", false}, {"@no", false}} {
		c := AlertsConfig{PublicEnabled: true, PublicChannel: tc.channel}
		err := c.Validate()
		if (err == nil) != tc.valid {
			t.Errorf("Validate(%q) error=%v, valid=%v", tc.channel, err, tc.valid)
		}
	}
}

func TestAlertsConfigPatchKeepsOmittedSwitches(t *testing.T) {
	base := Default()
	disabled := false
	got := (AlertsConfigPatch{Events: &EventsPatch{Node: &disabled}}).Merge(base)
	if got.Events.Node || !got.Events.Warp || !got.Events.Inbound || !got.Events.Update {
		t.Fatalf("partial patch lost existing event switches: %+v", got.Events)
	}
}

func TestAppendPendingDeduplicates(t *testing.T) {
	a := delivery{Key: "same", Target: "admin", Text: "one"}
	got := appendPending([]delivery{a}, delivery{Key: "same", Target: "admin", Text: "two"})
	if len(got) != 1 || got[0].Text != "one" {
		t.Fatalf("duplicate queue item: %+v", got)
	}
}
