package extensionControllers

import (
	"errors"
	"strings"
	"testing"

	"github.com/FloMorphic/morph-api/models"
	InfraSpaces "github.com/Inflowenger/inflow-fusion/spaces"
)

// A multi (open) credential carries no pluginId by design — the settings page
// mints it from a name alone. The strict permission builder rejects an id it
// cannot place in a subject, and the empty string is one, so building the open
// permission must not go through it: doing so failed every "generate multi
// plugin" request with an invalid-pluginId error.
func TestCredPermissionOpenNeedsNoPluginId(t *testing.T) {
	perm, err := credPermission(models.CredRequest{
		Name:   "my-open-cred",
		Access: models.MultiPluginAccess,
	}, "ACCPUB")
	if err != nil {
		t.Fatalf("open permission with no pluginId: %v", err)
	}
	if perm.Name != "my-open-cred" || perm.Account != "ACCPUB" {
		t.Errorf("got name %q account %q", perm.Name, perm.Account)
	}
	// Empty allow-lists are what NATS reads as "allow all"; a single publish
	// subject here would narrow the credential to only that subject.
	if len(perm.Pub.Allow) != 0 || len(perm.Sub.Allow) != 0 {
		t.Errorf("open credential must leave allow-lists empty, got pub %v sub %v",
			perm.Pub.Allow, perm.Sub.Allow)
	}
}

// Strict credentials enumerate their publish subjects, so the svc reach has to
// be listed explicitly or the plugin cannot talk to the OC proxy / svc handlers.
func TestCredPermissionStrictScopesToPluginAndSvc(t *testing.T) {
	perm, err := credPermission(models.CredRequest{
		Name:     "llm",
		PluginId: "aaaa-bbbb-cccc-llm0",
		Access:   models.StrictAccess,
	}, "ACCPUB")
	if err != nil {
		t.Fatalf("strict permission: %v", err)
	}
	for _, want := range []string{"flomorphic.svc.>", "_INBOX.>"} {
		if !hasSubject(perm.Pub.Allow, want) {
			t.Errorf("strict pub allow missing %q, got %v", want, perm.Pub.Allow)
		}
	}
	if !subjectMentions(perm.Sub.Allow, "aaaa-bbbb-cccc-llm0") {
		t.Errorf("strict sub allow is not scoped to the plugin id, got %v", perm.Sub.Allow)
	}
}

// A pluginId that cannot be a single subject token is request input, so it must
// surface as a caller error (400) rather than an infra failure (500).
func TestCredPermissionStrictRejectsUnusablePluginId(t *testing.T) {
	for _, id := range []string{"", "has.dot", "wild*", "all>"} {
		_, err := credPermission(models.CredRequest{
			Name:     "x",
			PluginId: id,
			Access:   models.StrictAccess,
		}, "ACCPUB")
		if err == nil {
			t.Errorf("pluginId %q: expected rejection", id)
			continue
		}
		if !errors.Is(err, errBadCredRequest) {
			t.Errorf("pluginId %q: not reported as a caller error: %v", id, err)
		}
		if !errors.Is(err, InfraSpaces.ErrInvalidPluginId) {
			t.Errorf("pluginId %q: lost the underlying cause: %v", id, err)
		}
	}
}

func hasSubject(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func subjectMentions(list []string, token string) bool {
	for _, s := range list {
		if strings.Contains(s, token) {
			return true
		}
	}
	return false
}
