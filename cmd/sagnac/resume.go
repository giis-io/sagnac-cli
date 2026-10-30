package main

import (
	"time"

	"github.com/giis-io/sagnac-cli/internal/api"
	"github.com/giis-io/sagnac-cli/internal/i18n"
)

// resumeAt is when the platform accepts the request again, as a 429 says it
// (docs/api/erreurs.md): retry_at when it is a plausible instant (after now,
// within api.MaxDelay), else now plus Retry-After; the zero time when the
// answer gives neither. It is shown, never waited for.
func resumeAt(e *api.APIError, now time.Time) time.Time {
	if at := e.RetryAt; !at.IsZero() && at.After(now) && !at.After(now.Add(api.MaxDelay)) {
		return at
	}
	if e.RetryAfter > 0 {
		return now.Add(e.RetryAfter)
	}
	return time.Time{}
}

// resumeText writes at in the local time zone, rounded up to the minute (a
// time shown must not come before the real one): the hour alone on the day
// of now, the date and the hour otherwise.
// localZone is the zone resumeText writes in; tests replace it rather than
// time.Local, which other goroutines read.
var localZone = time.Local

func resumeText(at, now time.Time) string {
	local := at.In(localZone)
	if r := local.Truncate(time.Minute); !r.Equal(local) {
		local = r.Add(time.Minute)
	}
	today := now.In(localZone)
	if local.Year() == today.Year() && local.YearDay() == today.YearDay() {
		return i18n.T("resume.today", local.Format("15:04"))
	}
	return i18n.T("resume.date", local.Format("02/01/2006"), local.Format("15:04"))
}
