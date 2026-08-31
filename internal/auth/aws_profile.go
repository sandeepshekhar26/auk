package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"apitool/internal/core/model"
)

// AWS profile and SSO credential bridging.
//
// The SigV4 signer next door has always worked; what it lacked was a way to
// get credentials the way anyone actually holds them in 2026. Pasting a
// long-lived AKIA/secret pair into a GUI is precisely what every security team
// tells people not to do, and for an SSO-based organisation it is not even
// possible — the credentials are temporary and rotate hourly.
//
// So: name a profile, and AUK asks the user's OWN AWS CLI for credentials.
// This is the internal/onepassword philosophy applied again — shell out to the
// tool the user already installed and authenticated, hold nothing, bundle no
// SDK. `aws configure export-credentials` handles every source the CLI knows
// (static profiles, SSO sessions, assumed roles, instance metadata, external
// credential processes) which means AUK inherits all of them for the price of
// one subprocess and never learns what any of them are.

// awsCredentials is the shape `aws configure export-credentials --format json`
// returns. Field names are AWS's, not ours.
type awsCredentials struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken,omitempty"`
	// Expiration is RFC3339 and absent for non-temporary credentials.
	Expiration string `json:"Expiration,omitempty"`
}

// awsCredCache memoises exported credentials per profile until shortly before
// they expire.
//
// Without it every send shells out to `aws`, which for an SSO profile is a
// ~200-400ms subprocess — turning a 50-request folder run into 20 seconds of
// waiting on a CLI to tell us the same answer fifty times.
type awsCredCache struct {
	mu sync.Mutex
	m  map[string]awsCachedCred
}

type awsCachedCred struct {
	cred      awsCredentials
	expiresAt time.Time // zero = no expiry reported
}

// awsExpirySkew retires a credential slightly early, so one cannot expire
// between being signed with and reaching AWS.
const awsExpirySkew = 60 * time.Second

var awsCreds = &awsCredCache{m: map[string]awsCachedCred{}}

func (c *awsCredCache) get(profile string) (awsCredentials, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[profile]
	if !ok {
		return awsCredentials{}, false
	}
	if !e.expiresAt.IsZero() && time.Until(e.expiresAt) <= awsExpirySkew {
		delete(c.m, profile)
		return awsCredentials{}, false
	}
	return e.cred, true
}

func (c *awsCredCache) put(profile string, cred awsCredentials) {
	var exp time.Time
	if cred.Expiration != "" {
		if t, err := time.Parse(time.RFC3339, cred.Expiration); err == nil {
			exp = t
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[profile] = awsCachedCred{cred: cred, expiresAt: exp}
}

// awsCLIAvailable reports whether the AWS CLI is on PATH.
func awsCLIAvailable() bool {
	_, err := exec.LookPath("aws")
	return err == nil
}

// exportAWSCredentials asks the AWS CLI for usable credentials for profile.
//
// `configure export-credentials` is AWS CLI v2's own supported way to hand
// credentials to another program, so this resolves SSO sessions, assumed
// roles and credential processes without AUK understanding any of them.
func exportAWSCredentials(ctx context.Context, profile string) (awsCredentials, error) {
	if !awsCLIAvailable() {
		return awsCredentials{}, fmt.Errorf(
			"AWS CLI not found on PATH — install it (https://aws.amazon.com/cli/) to sign with profile %q, or paste an access key instead", profile)
	}

	args := []string{"configure", "export-credentials", "--format", "process"}
	if profile != "" {
		args = append(args, "--profile", profile)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "aws", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		// The CLI's own message is better than anything guessable here: it
		// already says "SSO session has expired, run aws sso login" and names
		// the profile. Passed through unchanged, with one hint appended for
		// the single most common case.
		if strings.Contains(strings.ToLower(msg), "sso") || strings.Contains(strings.ToLower(msg), "expired") {
			return awsCredentials{}, fmt.Errorf("AWS profile %q: %s\n(try: aws sso login --profile %s)", profile, msg, profile)
		}
		// The profile is named even when the CLI's own message does not
		// mention it — a bare "signal: killed" or "exit status 1" leaves the
		// user with no idea which of their profiles is the broken one.
		return awsCredentials{}, fmt.Errorf("AWS profile %q: aws configure export-credentials failed: %s", profile, msg)
	}

	var cred awsCredentials
	if err := json.Unmarshal(stdout.Bytes(), &cred); err != nil {
		return awsCredentials{}, fmt.Errorf("could not read credentials from the AWS CLI: %w", err)
	}
	if cred.AccessKeyID == "" || cred.SecretAccessKey == "" {
		return awsCredentials{}, fmt.Errorf("the AWS CLI returned no credentials for profile %q", profile)
	}
	return cred, nil
}

// resolveAWSCredentials fills in cfg's credentials from the named profile when
// one is set, leaving an explicitly-pasted key pair alone.
//
// Precedence is deliberate: a profile WINS over pasted keys when both are
// present. A config carrying both is one someone migrated from static keys to
// a profile, and honouring the stale keys would sign with credentials they
// believe they stopped using.
func resolveAWSCredentials(ctx context.Context, cfg model.AWSSigV4Auth) (model.AWSSigV4Auth, error) {
	if strings.TrimSpace(cfg.Profile) == "" {
		return cfg, nil
	}
	profile := strings.TrimSpace(cfg.Profile)

	cred, ok := awsCreds.get(profile)
	if !ok {
		fresh, err := exportAWSCredentials(ctx, profile)
		if err != nil {
			return cfg, err
		}
		awsCreds.put(profile, fresh)
		cred = fresh
	}

	out := cfg
	out.AccessKeyID = cred.AccessKeyID
	out.SecretAccessKey = cred.SecretAccessKey
	out.SessionToken = cred.SessionToken
	return out, nil
}
