// Copyright (c) 2026 Daniel Joppi (https://github.com/swarm-routing)
// SPDX-License-Identifier: MIT OR GPL-2.0-only

// Package rotate is P7: the one GitLab token (scope `api`, stored as GITLAB_TOKEN in
// both environments, the maintainer's decision of 2026-11-16) is rotated once it is
// 60 days old, or expires within 14 days, with GitLab's self-rotation (which revokes
// the old token), given a 90-day expiry, and stored in both environments with the
// org App's token (a libsodium sealed box for the repository environment's public
// key). The App's access is checked first: its read of each environment's public
// key, then a write of the current token back into each, so a token is never rotated
// that could not be stored; a missing permission or any failure opens an issue
// instead of failing silently. No token text is ever printed.
package rotate

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/nacl/box"

	"github.com/swarm-routing/ns3-upstream-report/internal/bridge"
	"github.com/swarm-routing/ns3-upstream-report/internal/github"
	"github.com/swarm-routing/ns3-upstream-report/internal/gitlabwrite"
	"github.com/swarm-routing/ns3-upstream-report/internal/httpx"
	"github.com/swarm-routing/ns3-upstream-report/internal/py"
)

// The rotation policy.
const (
	After    = 60 * 24 * time.Hour
	Lead     = 14 * 24 * time.Hour
	Lifetime = 90 * 24 * time.Hour
)

// Environments hold the one token.
var Environments = []string{"gitlab-read", "gitlab-write"}

// Encrypt seals a secret for a base64 public key.
type Encrypt func(publicKey, value string) (string, error)

// SealedBox is libsodium's crypto_box_seal, as GitHub's secrets API wants it.
func SealedBox(publicKey, value string) (string, error) {
	k, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil || len(k) != 32 {
		return "", errors.New("the environment's public key is not a 32-byte base64 key")
	}
	var pk [32]byte
	copy(pk[:], k)
	out, err := box.SealAnonymous(nil, []byte(value), &pk, rand.Reader)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(out), nil
}

// Rotator runs P7.
type Rotator struct {
	GW      *gitlabwrite.Client
	App     *github.Client // the org App's token (nil: not available)
	GH      *github.Client // this repository's issues
	Encrypt Encrypt
	Now     time.Time
	Dry     bool
}

var glpat = regexp.MustCompile(`glpat-[A-Za-z0-9_.-]+`)

func (r *Rotator) redact(s string, secrets ...string) string {
	for _, x := range append(secrets, r.GW.R.Token) {
		if x != "" {
			s = strings.ReplaceAll(s, x, "[redacted]")
		}
	}
	return py.Head(glpat.ReplaceAllString(s, "glpat-[redacted]"), 300)
}

// issue opens (once while open) an issue with this title.
func (r *Rotator) issue(title, body string) (string, error) {
	issues, err := r.GH.Issues()
	if err != nil {
		return "", err
	}
	for _, i := range issues {
		if py.Str(i["title"]) == title && py.Str(i["state"]) == "open" {
			return fmt.Sprintf("#%d", bridge.Number(i)), nil
		}
	}
	got, err := r.GH.CreateIssue(title, body+bridge.Footer, []string{"bug"})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("#%d", bridge.Number(got)), nil
}

type key struct{ id, key string }

func (r *Rotator) store(keys map[string]key, value string) error {
	for _, env := range Environments {
		enc, err := r.Encrypt(keys[env].key, value)
		if err != nil {
			return err
		}
		if _, err := r.App.Call("PUT", fmt.Sprintf("repos/%s/environments/%s/secrets/GITLAB_TOKEN", r.App.Repo, env),
			map[string]any{"encrypted_value": enc, "key_id": keys[env].id}); err != nil {
			return fmt.Errorf("storing it in %s: %w", env, err)
		}
	}
	return nil
}

// Run is one P7 pass.
func (r *Rotator) Run() ([]string, error) {
	if r.GW.R.Token == "" {
		return []string{"P7: skipped: no GitLab token in this environment (the one-time bootstrap)"}, nil
	}
	if r.App == nil {
		n, err := r.issue("[P7] the GitLab token rotation has no org App token",
			"P7 (`rotate.yml`) needs the org App's token (`BOT_APP_ID`, `BOT_APP_PRIVATE_KEY`, through the org's `bot-token` action) "+
				"to store the rotated GitLab token as an environment secret, and the run had none. Make those secrets available "+
				"to this repository; until then the token is not rotated, and read warns 30 days before it expires.")
		if err != nil {
			return nil, err
		}
		return []string{"P7: no org App token; " + n + " says so"}, nil
	}
	keys := map[string]key{}
	for _, env := range Environments {
		v, err := r.App.Call("GET", fmt.Sprintf("repos/%s/environments/%s/secrets/public-key", r.App.Repo, env), nil)
		if err != nil {
			st := httpx.Status(err)
			if st == 401 || st == 403 || st == 404 {
				n, ierr := r.issue("[P7] the org App cannot store the rotated GitLab token",
					fmt.Sprintf("P7 (`rotate.yml`) checked the org App's access before rotating anything: reading the secrets public key "+
						"of environment `%s` returned HTTP %d.\n\nMissing permission: the App's repository permission **Secrets: Read and "+
						"write** on this repository (environment secrets; a 404 can also mean the environment does not exist, or the App is "+
						"not installed here). Nothing was rotated.", env, st))
				if ierr != nil {
					return nil, ierr
				}
				return []string{fmt.Sprintf("P7: the org App cannot read %s's secrets public key (HTTP %d); %s says so; nothing rotated", env, st, n)}, nil
			}
			return nil, errors.New(r.redact(err.Error()))
		}
		m, _ := v.(map[string]any)
		keys[env] = key{py.Str(m["key_id"]), py.Str(m["key"])}
	}
	me, err := r.GW.R.TokenSelf()
	if err != nil {
		return nil, errors.New(r.redact(err.Error()))
	}
	created, err := bridge.ParseTime(py.Str(me["created_at"]))
	if err != nil {
		return nil, fmt.Errorf("token created_at: %v", err)
	}
	age := r.Now.Sub(created)
	exp := ""
	if s, ok := me["expires_at"].(string); ok {
		exp = s
	}
	soon := false
	if exp != "" {
		if t, err := time.Parse("2006-01-02", exp); err == nil && t.Sub(r.Now) <= Lead {
			soon = true
		}
	}
	if age < After && !soon {
		return []string{fmt.Sprintf("P7: token is %d days old (expires %s); rotates at %d", py.Days(age), or(exp, "never"), py.Days(After))}, nil
	}
	if r.Dry {
		return []string{fmt.Sprintf("P7: token is %d days old; dry run: would check the App can write both environments, rotate, and store", py.Days(age))}, nil
	}
	// the App's write, proved by storing the current value again, before anything is revoked
	if err := r.store(keys, r.GW.R.Token); err != nil {
		n, ierr := r.issue("[P7] the org App cannot store the rotated GitLab token",
			"P7 (`rotate.yml`) checked the org App's access before rotating: writing the current token back failed: "+
				r.redact(err.Error())+"\n\nMissing permission: the App's repository permission **Secrets: Read and write** on this "+
				"repository. Nothing was rotated.")
		if ierr != nil {
			return nil, ierr
		}
		return []string{"P7: the org App cannot write the environment secrets; " + n + " says so; nothing rotated"}, nil
	}
	newTok, err := r.GW.RotateSelf(r.Now.Add(Lifetime).Format("2006-01-02"))
	if err != nil {
		n, ierr := r.issue("[P7] GitLab token rotation failed",
			fmt.Sprintf("GitLab's self-rotation of the token failed on %s: %s\n\nThe token in the environments is unchanged; "+
				"P7 retries on its next run, and read warns 30 days before the token expires.", r.Now.Format("2006-01-02"), r.redact(err.Error())))
		if ierr != nil {
			return nil, ierr
		}
		return []string{"P7: rotation failed; " + n + " says so"}, nil
	}
	if err := r.store(keys, newTok); err != nil {
		n, ierr := r.issue("[P7] the rotated GitLab token could not be stored",
			fmt.Sprintf("GitLab rotated the token on %s (the old one is revoked), but storing the new one failed: %s\n\n"+
				"Every pipeline that reads or writes GitLab with the token fails until a new token (`api`) is stored as "+
				"`GITLAB_TOKEN` in both environments (the one-time bootstrap step).", r.Now.Format("2006-01-02"), r.redact(err.Error(), newTok)))
		if ierr != nil {
			return nil, ierr
		}
		return []string{"P7: rotated, but storing failed; " + n + " says so"}, nil
	}
	return []string{fmt.Sprintf("P7: rotated (the token was %d days old), stored in %s; expires %s",
		py.Days(age), strings.Join(Environments, " and "), r.Now.Add(Lifetime).Format("2006-01-02"))}, nil
}

func or(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
