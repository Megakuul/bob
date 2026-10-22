package buildctl

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/megakuul/bob/internal/forge"
	"github.com/megakuul/bob/internal/store"
)

// statusWindow is how long a status is retried after the build finished (§6.9.4: ≤ 24 h).
const statusWindow = 24 * time.Hour

// Status retry backoff: 5 s doubling to 5 min (§6.9.4).
const (
	statusBackoffMin = 5 * time.Second
	statusBackoffMax = 5 * time.Minute
)

// ContextEvaluate is the status context of the evaluation.
const ContextEvaluate = "bob/evaluate"

// Statuses are the commit statuses build rec should show now, by context (§6.9.4): bob/evaluate
// pending while queued, then success/failure; one bob/<pipeline> per matched pipeline.
func Statuses(rec *store.BuildRecord, webURL string) map[string]forge.Status {
	sha := rec.Commit
	if sha == "" {
		sha = rec.Source.SHA
	}
	if sha == "" || !shaRE.MatchString(sha) {
		return nil
	}
	desc := func(state string) string {
		if rec.State == StateSuperseded && rec.SupersededBy > 0 {
			return fmt.Sprintf("#%d superseded by #%d", rec.Number, rec.SupersededBy)
		}
		d := fmt.Sprintf("#%d %s", rec.Number, state)
		if len(rec.Errors) > 0 {
			e := rec.Errors[0]
			msg := e.Message
			if e.Code != "" && !containsCode(msg, e.Code) {
				msg = e.Code + " " + msg
			}
			d += ": " + msg
		}
		return d
	}
	st := func(ctx string, s forge.State, state string) forge.Status {
		return forge.Status{SHA: sha, Context: ctx, State: s, Description: desc(state), TargetURL: webURL}
	}
	out := map[string]forge.Status{}
	switch {
	case rec.Evaluated && rec.State == StateSkipped:
		out[ContextEvaluate] = forge.Status{SHA: sha, Context: ContextEvaluate, State: forge.StateSuccess,
			Description: fmt.Sprintf("#%d skipped: no pipeline matched", rec.Number), TargetURL: webURL}
	case rec.Evaluated:
		out[ContextEvaluate] = st(ContextEvaluate, forge.StateSuccess, "evaluated")
	case rec.State == StatePending:
	default:
		out[ContextEvaluate] = st(ContextEvaluate, forgeState(rec.State), rec.State)
	}
	for _, p := range rec.Pipelines {
		state := p.State
		if Terminal(rec.State) {
			state = rec.State
		} else if rec.State == StateQueued {
			state = StateQueued
		}
		out["bob/"+p.Name] = st("bob/"+p.Name, forgeState(state), state)
	}
	return out
}

func containsCode(s, code string) bool { return codeRE.FindString(s) == code }

// forgeState maps a build state to a commit status state.
func forgeState(state string) forge.State {
	switch state {
	case StatePending, StateQueued:
		return forge.StatePending
	case StateRunning:
		return forge.StateRunning
	case StateSucceeded, StateSkipped:
		return forge.StateSuccess
	case StateFailed:
		return forge.StateFailure
	case StateCancelled, StateSuperseded:
		return forge.StateCancelled
	}
	return forge.StateError
}

// WebURL is the build page of rec ("" without a base URL).
func (b *Builds) WebURL(rec *store.BuildRecord) string {
	if b.cfg.BaseURL == "" {
		return ""
	}
	return b.cfg.BaseURL + "/p/" + rec.Project + "/builds/" + rec.ID
}

// report sends the statuses of rec that differ from BuildRecord.rp and records what was sent.
// It reports whether every status is delivered.
func (b *Builds) report(ctx context.Context, rec *store.BuildRecord) bool {
	if b.cfg.Statuses == nil {
		return true
	}
	want := Statuses(rec, b.WebURL(rec))
	sent := map[string]string{}
	settled := true
	now := b.now()
	for _, c := range slices.Sorted(maps.Keys(want)) {
		s := want[c]
		mark := string(s.State) + " " + s.Description
		if rec.Reported[c] == mark {
			continue
		}
		key := rec.ID + "|" + c
		b.mu.Lock()
		r := b.retry[key]
		b.mu.Unlock()
		if now.Before(r.next) {
			settled = false
			continue
		}
		if err := b.cfg.Statuses.SetStatus(ctx, rec.Project, s); err != nil {
			settled = false
			r.delay = min(max(2*r.delay, statusBackoffMin), statusBackoffMax)
			r.next = now.Add(r.delay)
			b.mu.Lock()
			b.retry[key] = r
			b.mu.Unlock()
			b.cfg.Log.Warn("build: commit status not written; retrying", "build", rec.ID, "context", c, "in", r.delay, "err", err.Error())
			continue
		}
		b.mu.Lock()
		delete(b.retry, key)
		b.mu.Unlock()
		sent[c] = mark
	}
	if len(sent) == 0 {
		return settled
	}
	if _, err := b.update(ctx, rec.ID, func(cur *store.BuildRecord) error {
		if cur.Reported == nil {
			cur.Reported = map[string]string{}
		}
		for c, m := range sent {
			cur.Reported[c] = m
		}
		return nil
	}); err != nil {
		return false
	}
	return settled
}

// statusRetry is the backoff state of one undelivered status.
type statusRetry struct {
	next  time.Time
	delay time.Duration
}
