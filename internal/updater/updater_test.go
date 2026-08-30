package updater

import (
	"context"
	"errors"
	"testing"
)

// stubFeed is an in-memory Feed for exercising the Check decision without a
// network.
type stubFeed struct {
	rel Release
	err error
}

func (s stubFeed) Latest(context.Context) (Release, error) { return s.rel, s.err }

func newerRelease() Release {
	return Release{Version: "0.4.0", URL: "http://x/AUK-0.4.0.dmg", Notes: "new stuff", SizeBytes: 42}
}

func TestServiceCheck_UpdateAvailable(t *testing.T) {
	svc := &Service{Feed: stubFeed{rel: newerRelease()}, currentVersion: "0.3.0"}
	st, err := svc.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !st.Available {
		t.Error("Available = false, want true (0.4.0 > 0.3.0)")
	}
	if st.CurrentVersion != "0.3.0" || st.LatestVersion != "0.4.0" {
		t.Errorf("versions = current %q latest %q", st.CurrentVersion, st.LatestVersion)
	}
	if st.SizeBytes != 42 || st.URL == "" || st.Notes == "" {
		t.Errorf("Check dropped release metadata: %+v", st)
	}
}

func TestServiceCheck_UpToDate(t *testing.T) {
	svc := &Service{Feed: stubFeed{rel: newerRelease()}, currentVersion: "0.4.0"}
	st, _ := svc.Check(context.Background())
	if st.Available {
		t.Error("Available = true, want false (already on 0.4.0)")
	}
}

func TestServiceCheck_CurrentAheadOfFeed(t *testing.T) {
	// e.g. a wails-dev bundle at 1.0.0 vs a real 0.4.0 latest.
	svc := &Service{Feed: stubFeed{rel: newerRelease()}, currentVersion: "1.0.0"}
	st, _ := svc.Check(context.Background())
	if st.Available {
		t.Error("Available = true, want false (1.0.0 is ahead of 0.4.0)")
	}
}

func TestServiceCheck_DevBuildNeverNags(t *testing.T) {
	for _, dev := range []string{"", "0.0.0-dev"} {
		svc := &Service{Feed: stubFeed{rel: newerRelease()}, currentVersion: dev}
		st, err := svc.Check(context.Background())
		if err != nil {
			t.Fatalf("Check(dev=%q): %v", dev, err)
		}
		if st.Available {
			t.Errorf("dev build %q: Available = true, want false (never nag in dev)", dev)
		}
		if !st.IsDevBuild {
			t.Errorf("dev build %q: IsDevBuild = false, want true", dev)
		}
	}
}

func TestServiceCheck_FeedErrorIsSoft(t *testing.T) {
	// A rate-limited/offline feed must NOT crash or hard-error the launch check.
	svc := &Service{Feed: stubFeed{err: errors.New("rate-limited")}, currentVersion: "0.3.0"}
	st, err := svc.Check(context.Background())
	if err != nil {
		t.Errorf("Check returned a hard error for a network failure: %v", err)
	}
	if st.Available {
		t.Error("Available = true despite the feed failing")
	}
	if st.Error == "" {
		t.Error("Status.Error should record the soft failure reason")
	}
}

func TestServiceCheck_MalformedLatestNoUpdate(t *testing.T) {
	svc := &Service{Feed: stubFeed{rel: Release{Version: "not-a-version", URL: "http://x"}}, currentVersion: "0.3.0"}
	st, _ := svc.Check(context.Background())
	if st.Available {
		t.Error("Available = true for an unparseable latest version")
	}
}
