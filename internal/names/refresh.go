package names

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

// RefreshBudget bounds one refresh attempt (both GETs + verify).
const RefreshBudget = 5 * time.Second

// GetBaseURL derives the per-goatnet download-surface origin
// (https://get.<site>.<zone>) from the bundle's site id and the mesh
// zone — the same convention the goat-get role serves
// (get.{{ site_id }}.{{ site_dns_zone }}). Returns "" when either part
// is missing.
func GetBaseURL(siteID, zone string) string {
	siteID = strings.TrimSpace(siteID)
	zone = strings.TrimSpace(strings.TrimSuffix(zone, "."))
	if siteID == "" || zone == "" {
		return ""
	}
	return fmt.Sprintf("https://get.%s.%s", siteID, zone)
}

// Refresh fetches the artifact pair from baseURL and stores it under the
// monotonic-serial rule. Best-effort by design: refresh only matters
// while names (and the get tier) work, so failures are logged at most.
// Returns the accepted snapshot; ErrNotNewer when the cache was already
// as-new-or-newer (the quiet common case).
func (st *Store) Refresh(ctx context.Context, client *http.Client, baseURL string) (*Snapshot, error) {
	if baseURL == "" {
		return nil, fmt.Errorf("no get-base URL")
	}
	if client == nil {
		client = &http.Client{Timeout: RefreshBudget}
	}
	ctx, cancel := context.WithTimeout(ctx, RefreshBudget)
	defer cancel()
	artifact, err := fetch(ctx, client, strings.TrimSuffix(baseURL, "/")+"/"+SnapshotFile)
	if err != nil {
		return nil, err
	}
	sig, err := fetch(ctx, client, strings.TrimSuffix(baseURL, "/")+"/"+SigFile)
	if err != nil {
		return nil, err
	}
	accepted, err := st.PutSnapshot(artifact, sig)
	if errors.Is(err, ErrNotNewer) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("fetched artifact refused: %w", err)
	}
	if accepted != nil {
		log.Printf("names: cached snapshot serial %d (site %s, zone %s, %d records) from %s",
			accepted.Serial, accepted.SiteID, accepted.Zone, len(accepted.Records), baseURL)
	}
	// Claims ride the same tier, best-effort and without serial semantics
	// (each claim is individually leaf-signed, verified at read). Absent
	// claims.json is the normal pre-Amendment-2 case.
	if claims, cerr := fetch(ctx, client, strings.TrimSuffix(baseURL, "/")+"/"+ClaimsFile); cerr == nil {
		if perr := st.PutClaims(claims); perr != nil {
			log.Printf("names: fetched claims.json refused: %v", perr)
		}
	}
	return accepted, nil
}

func fetch(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	// The artifact is a few KB; 1 MiB is a generous ceiling.
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
