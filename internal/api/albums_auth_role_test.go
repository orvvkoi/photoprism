package api

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/photoprism/photoprism/internal/auth/acl"
	"github.com/photoprism/photoprism/internal/entity"
	"github.com/photoprism/photoprism/pkg/authn"
)

// mixedPrincipalSession builds a restricted client session acting for a privileged account: an
// instance client (GrantSearchShared) owned by the admin user alice.
func mixedPrincipalSession() *entity.Session {
	s := &entity.Session{}
	s.SetClient(&entity.Client{ClientRole: acl.RoleInstance.String(), AuthProvider: authn.ProviderClient.String()})
	s.SetUser(entity.UserFixtures.Pointer("alice"))
	s.GetUser().RefreshShares()
	return s
}

func TestAlbumViewableBySessionEffectiveRole(t *testing.T) {
	// An album the owning account neither created nor shares.
	album := entity.AlbumFixtures.Get("holiday-2030")

	t.Run("AdminMayView", func(t *testing.T) {
		s := &entity.Session{}
		s.SetUser(entity.UserFixtures.Pointer("alice"))
		assert.True(t, albumViewableBySession(s, album))
	})
	t.Run("MixedPrincipalMayNot", func(t *testing.T) {
		s := mixedPrincipalSession()
		assert.False(t, s.GetUser().HasSharedAccessOnly(acl.ResourceAlbums))
		assert.True(t, s.HasSharedAccessOnly(acl.ResourceAlbums))
		assert.False(t, albumViewableBySession(s, album))
	})
}

func TestAlbumShareRequiredEffectiveRole(t *testing.T) {
	t.Run("AdminNeedsNoShare", func(t *testing.T) {
		s := &entity.Session{}
		s.SetUser(entity.UserFixtures.Pointer("alice"))
		assert.False(t, albumShareRequired(s, "as6sg6bxpogaaba8"))
	})
	t.Run("MixedPrincipalNeedsShare", func(t *testing.T) {
		assert.True(t, albumShareRequired(mixedPrincipalSession(), "as6sg6bxpogaaba8"))
	})
}
