package subscription

import (
	"strings"
	"testing"
)

func TestParseJSONArrayOfURIs(t *testing.T) {
	body := []byte(`["vless://00000000-0000-0000-0000-000000000001@example.com:443?security=tls#one"]`)
	servers, errs := Parse(body)
	if len(errs) != 0 || len(servers) != 1 || servers[0].Name != "one" {
		t.Fatalf("servers=%+v errs=%v", servers, errs)
	}
}

func TestEmptyReasonDoesNotEchoResponse(t *testing.T) {
	secret := "secret-value"
	reason := EmptyReason([]byte("broken-"+secret), []error{errWithSecret(secret)})
	if strings.Contains(reason, secret) {
		t.Fatal("diagnostic leaked response")
	}
}

type secretError string

func (e secretError) Error() string { return string(e) }
func errWithSecret(s string) error  { return secretError(s) }
