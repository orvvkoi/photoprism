package webdav

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/photoprism/photoprism/pkg/fs"
)

// downloadTestClient returns a client for a server that serves body on GET, and calls onGet before
// answering so a test can change the destination while the request is in flight.
func downloadTestClient(t *testing.T, body string, onGet func()) *Client {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if onGet != nil {
			onGet()
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))

	t.Cleanup(server.Close)

	c, err := NewClient(server.URL+"/", "", "", TimeoutLow, "")
	require.NoError(t, err)

	return c
}

func TestClient_DownloadCreatesExclusively(t *testing.T) {
	t.Run("DestinationAppearingDuringTheRequestIsKept", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "local.bin")

		// A file created while the request is in flight must be kept: the destination is created
		// exclusively, so this call never replaces it.
		client := downloadTestClient(t, "remote", func() {
			require.NoError(t, os.WriteFile(dest, []byte("local"), fs.ModeFile))
		})

		err := client.Download("/local.bin", dest, false)
		require.Error(t, err)

		got, readErr := os.ReadFile(dest) //nolint:gosec // test fixture reads a test-owned temporary path
		require.NoError(t, readErr)
		assert.Equal(t, "local", string(got), "the local file must not be replaced")
	})
	t.Run("ExistingDestinationIsRefused", func(t *testing.T) {
		// Guards the early existence check rather than the exclusive create; the subtest above is
		// what discriminates the open.
		dest := filepath.Join(t.TempDir(), "local.bin")
		require.NoError(t, os.WriteFile(dest, []byte("local"), fs.ModeFile))

		client := downloadTestClient(t, "remote", nil)
		require.Error(t, client.Download("/local.bin", dest, false))

		got, readErr := os.ReadFile(dest) //nolint:gosec // test fixture reads a test-owned temporary path
		require.NoError(t, readErr)
		assert.Equal(t, "local", string(got))
	})
	t.Run("NewDestinationIsWritten", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "new.bin")
		client := downloadTestClient(t, "remote", nil)
		require.NoError(t, client.Download("/new.bin", dest, false))

		got, readErr := os.ReadFile(dest) //nolint:gosec // test fixture reads a test-owned temporary path
		require.NoError(t, readErr)
		assert.Equal(t, "remote", string(got))
	})
	t.Run("ForcedReplacementSucceeds", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "local.bin")
		require.NoError(t, os.WriteFile(dest, []byte("local"), fs.ModeFile))

		client := downloadTestClient(t, "remote", nil)
		require.NoError(t, client.Download("/local.bin", dest, true))

		got, readErr := os.ReadFile(dest) //nolint:gosec // test fixture reads a test-owned temporary path
		require.NoError(t, readErr)
		assert.Equal(t, "remote", string(got))
		assert.Equal(t, []string{"local.bin"}, dirEntries(t, dir), "no temporary file may be left behind")
	})
}

func TestClient_DownloadLeavesNothingBehindOnFailure(t *testing.T) {
	// A response that promises more than it delivers, so the copy fails part way through.
	truncating := func(t *testing.T) *Client {
		t.Helper()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}

			w.Header().Set("Content-Length", "64")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("short"))

			if hijacker, ok := w.(http.Hijacker); ok {
				conn, _, hijackErr := hijacker.Hijack()

				if hijackErr == nil {
					_ = conn.Close()
				}
			}
		}))

		t.Cleanup(server.Close)

		c, err := NewClient(server.URL+"/", "", "", TimeoutLow, "")
		require.NoError(t, err)

		return c
	}

	t.Run("ForcedReplacementKeepsTheLocalFile", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "local.bin")
		require.NoError(t, os.WriteFile(dest, []byte("local"), fs.ModeFile))

		require.Error(t, truncating(t).Download("/local.bin", dest, true))

		got, readErr := os.ReadFile(dest) //nolint:gosec // test fixture reads a test-owned temporary path
		require.NoError(t, readErr)
		assert.Equal(t, "local", string(got), "a failed replacement must leave the local file intact")
		assert.Equal(t, []string{"local.bin"}, dirEntries(t, dir))
	})
	t.Run("NewDestinationIsRemoved", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "new.bin")

		err := truncating(t).Download("/new.bin", dest, false)
		// Anchored on the copy stage, so a failure before the file is created cannot satisfy it.
		require.ErrorContains(t, err, "failed writing")
		assert.Empty(t, dirEntries(t, dir), "a failed download must leave nothing behind")
	})
	t.Run("OversizeKeepsTheLocalFile", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "local.bin")
		require.NoError(t, os.WriteFile(dest, []byte("local"), fs.ModeFile))

		client := downloadTestClient(t, "a much longer remote body than the limit allows", nil)
		client.SetDownloadLimit(8)

		require.Error(t, client.Download("/local.bin", dest, true))

		got, readErr := os.ReadFile(dest) //nolint:gosec // test fixture reads a test-owned temporary path
		require.NoError(t, readErr)
		assert.Equal(t, "local", string(got))
		assert.Equal(t, []string{"local.bin"}, dirEntries(t, dir))
	})
}

// dirEntries returns the sorted names of the entries in dir.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))

	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names
}

func TestClient_DownloadDoesNotFollowSymlinks(t *testing.T) {
	t.Run("DanglingSymlinkIsRefused", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.bin")
		dest := filepath.Join(dir, "link.bin")
		require.NoError(t, os.Symlink(target, dest))

		// A symlink is not a destination this call may write through, and the target must not be
		// created on its behalf.
		require.Error(t, downloadTestClient(t, "remote", nil).Download("/link.bin", dest, false))
		assert.NoFileExists(t, target)
	})
	t.Run("ForcedReplacementLeavesTheTargetAlone", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.bin")
		dest := filepath.Join(dir, "link.bin")
		require.NoError(t, os.WriteFile(target, []byte("target"), fs.ModeFile))
		require.NoError(t, os.Symlink(target, dest))

		require.NoError(t, downloadTestClient(t, "remote", nil).Download("/link.bin", dest, true))

		got, readErr := os.ReadFile(target) //nolint:gosec // test fixture reads a test-owned temporary path
		require.NoError(t, readErr)
		assert.Equal(t, "target", string(got), "the link target keeps its contents")

		written, readErr := os.ReadFile(dest) //nolint:gosec // test fixture reads a test-owned temporary path
		require.NoError(t, readErr)
		assert.Equal(t, "remote", string(written))
	})
}

func TestClient_DownloadConcurrentDestination(t *testing.T) {
	t.Run("OnlyOneCreates", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "shared.bin")
		client := downloadTestClient(t, "remote", nil)

		var wg sync.WaitGroup
		results := make([]error, 8)

		for i := range results {
			wg.Add(1)

			go func() {
				defer wg.Done()
				results[i] = client.Download("/shared.bin", dest, false)
			}()
		}

		wg.Wait()

		created := 0

		for _, err := range results {
			if err == nil {
				created++
			}
		}

		assert.Equal(t, 1, created, "exactly one call may create the destination")
		assert.Equal(t, []string{"shared.bin"}, dirEntries(t, dir))
	})
	t.Run("ForcedReplacementsLeaveOneCompleteFile", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "shared.bin")
		client := downloadTestClient(t, "a complete remote body", nil)

		var wg sync.WaitGroup

		for range 8 {
			wg.Add(1)

			go func() {
				defer wg.Done()
				_ = client.Download("/shared.bin", dest, true)
			}()
		}

		wg.Wait()

		got, readErr := os.ReadFile(dest) //nolint:gosec // test fixture reads a test-owned temporary path
		require.NoError(t, readErr)
		assert.Equal(t, "a complete remote body", string(got), "the destination is never a partial write")
		assert.Equal(t, []string{"shared.bin"}, dirEntries(t, dir), "no staging file may survive")
	})
}

func TestClient_DownloadKeepsDestinationMode(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "restricted.bin")
	require.NoError(t, os.WriteFile(dest, []byte("local"), 0o600))
	require.NoError(t, os.Chmod(dest, 0o600))

	require.NoError(t, downloadTestClient(t, "remote", nil).Download("/restricted.bin", dest, true))

	info, err := os.Stat(dest)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "replacing a file must not widen its mode")
}
