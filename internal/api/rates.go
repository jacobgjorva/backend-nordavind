package api

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// usdNok henter og cacher USD/NOK-spotkursen fra Norges Banks åpne API.
type usdNok struct {
	mu        sync.Mutex
	rate      float64
	fetchedAt time.Time
}

const ratesURL = "https://data.norges-bank.no/api/data/EXR/B.USD.NOK.SP?lastNObservations=1&format=csv"

// get returnerer kursen, maks ett døgn gammel. 0 hvis utilgjengelig.
func (u *usdNok) get(ctx context.Context, client *http.Client) float64 {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.rate > 0 && time.Since(u.fetchedAt) < 24*time.Hour {
		return u.rate
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ratesURL, nil)
	if err != nil {
		return u.rate
	}
	resp, err := client.Do(req)
	if err != nil {
		return u.rate
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return u.rate
	}

	// Siste kolonne på siste datalinje er OBS_VALUE.
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 {
		return u.rate
	}
	fields := strings.Split(lines[len(lines)-1], ";")
	rate, err := strconv.ParseFloat(strings.TrimSpace(fields[len(fields)-1]), 64)
	if err != nil || rate <= 0 {
		return u.rate
	}
	u.rate = rate
	u.fetchedAt = time.Now()
	return u.rate
}
