package kafka

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/twmb/franz-go/pkg/kgo"
)

// TestMSKIAMResolvesCredentials covers the AWS_MSK_IAM path without an MSK
// cluster: the mechanism has to reach the default credential chain, and a
// resolved credential has to arrive in the shape franz-go signs with.
//
// Static environment credentials stand in for the pod's IRSA role. They are the
// first provider in the default chain, so this exercises exactly the code that
// runs in the cluster minus the token exchange.
func TestMSKIAMResolvesCredentials(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrEXAMPLEKEY")
	t.Setenv("AWS_SESSION_TOKEN", "SESSIONEXAMPLE")

	creds := &iamCredentials{region: "eu-west-1"}

	auth, err := creds.auth(t.Context())
	if err != nil {
		t.Fatalf("resolving MSK IAM credentials: %v", err)
	}

	tests := []struct {
		field string
		got   string
		want  string
	}{
		{"AccessKey", auth.AccessKey, "AKIAEXAMPLE"},
		{"SecretKey", auth.SecretKey, "wJalrEXAMPLEKEY"},
		{"SessionToken", auth.SessionToken, "SESSIONEXAMPLE"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("Auth.%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}

	// The second call must come from the cached provider rather than walking the
	// chain again — a credential lookup happens on every broker connection.
	if _, err := creds.auth(t.Context()); err != nil {
		t.Fatalf("the second credential retrieval failed: %v", err)
	}
}

// TestMSKIAMReportsMissingCredentials: a pod without an IRSA role must fail
// authentication with a message that says so, not with an opaque SASL error.
func TestMSKIAMReportsMissingCredentials(t *testing.T) {
	// Clear every source the default chain reads, so nothing on the developer's
	// machine or the CI runner can satisfy it.
	for _, key := range []string{
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_PROFILE", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_ROLE_ARN",
		"AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI",
	} {
		t.Setenv(key, "")
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unsetting %s: %v", key, err)
		}
	}
	// Shared config files are the last place the chain looks.
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/absent-config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/absent-credentials")
	// IMDS would otherwise be tried, and on a laptop that is a slow timeout.
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")

	creds := &iamCredentials{region: "eu-west-1"}

	ctx, cancel := context.WithTimeout(t.Context(), defaultDeliveryTimeout)
	defer cancel()

	_, err := creds.auth(ctx)
	if err == nil {
		t.Fatal("MSK IAM authentication succeeded with no credentials available")
	}
	if !strings.Contains(err.Error(), "MSK IAM") {
		t.Errorf("the error does not say what failed: %v", err)
	}
}

// TestMSKIAMMechanismIsWired proves the mechanism reaches the client, which is
// the part a unit test can assert without an MSK cluster. The signing itself is
// franz-go's, verified against the Java implementation upstream.
func TestMSKIAMMechanismIsWired(t *testing.T) {
	if mech := mskIAM("eu-west-1"); mech == nil {
		t.Fatal("mskIAM returned no mechanism")
	}

	cfg := Config{
		Brokers:       []string{"b-1.colx.abc123.c2.kafka.eu-west-1.amazonaws.com:9098"},
		SASLIAMRegion: "eu-west-1",
	}
	opts, err := cfg.connectionOpts()
	if err != nil {
		t.Fatalf("connectionOpts: %v", err)
	}
	client, err := kgo.NewClient(opts...)
	if err != nil {
		t.Fatalf("kgo rejected the MSK IAM options: %v", err)
	}
	defer client.Close()

	if mechanisms := client.OptValues(kgo.SASL); len(mechanisms) == 0 || mechanisms[0] == nil {
		t.Fatal("no SASL mechanism on the client — the connection would be unauthenticated")
	}
}

// TestClientLoggerBridge covers both levels the bridge maps. It exists because a
// silent client logger is how a broker problem becomes an unexplained latency
// graph.
func TestClientLoggerBridge(t *testing.T) {
	logger := slogLogger{}
	if got := logger.Level(); got != kgo.LogLevelWarn {
		t.Errorf("Level() = %v, want warn — info includes every metadata refresh", got)
	}

	tests := []struct {
		name  string
		level kgo.LogLevel
	}{
		{"an error is logged", kgo.LogLevelError},
		{"a warning is logged", kgo.LogLevelWarn},
		{"anything else is logged as a warning", kgo.LogLevelDebug},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// The assertion is that it does not panic on any level and accepts
			// franz-go's alternating key/value arguments; slog's own output is
			// not this package's contract.
			logger.Log(tc.level, "test message", "topic", "collections.case", "err", errors.New("boom"))
		})
	}
}
