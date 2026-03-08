package spotify

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	librespot "github.com/devgianlu/go-librespot"
	"github.com/devgianlu/go-librespot/audio"
	"github.com/gopxl/beep/v2"

	"cliamp/playlist"
)

// SpotifyProvider implements playlist.Provider using go-librespot's spclient
// for playlist/track metadata and audio streaming.
// playlistCache holds fetched tracks for a playlist, allowing us to skip
// re-fetching playlists that haven't changed.
type playlistCache struct {
	tracks []playlist.Track
}

type SpotifyProvider struct {
	session    *Session
	clientID   string
	mu         sync.Mutex
	trackCache map[string]*playlistCache // playlist ID → cache entry
}

// New creates a SpotifyProvider. If session is nil, authentication is
// deferred until the user first selects the Spotify provider.
func New(session *Session, clientID string) *SpotifyProvider {
	return &SpotifyProvider{
		session:    session,
		clientID:   clientID,
		trackCache: make(map[string]*playlistCache),
	}
}

// ensureSession tries to create a session using stored credentials only
// (no browser). Returns playlist.ErrNeedsAuth if interactive sign-in is needed.
func (p *SpotifyProvider) ensureSession() error {
	p.mu.Lock()
	if p.session != nil {
		p.mu.Unlock()
		return nil
	}
	clientID := p.clientID
	p.mu.Unlock()

	if clientID == "" {
		return fmt.Errorf("spotify: no client ID available")
	}
	sess, err := NewSessionSilent(context.Background(), clientID)
	if err != nil {
		return playlist.ErrNeedsAuth
	}
	p.mu.Lock()
	p.session = sess
	p.mu.Unlock()
	return nil
}

// Authenticate runs the interactive sign-in flow (opens browser, waits for callback).
func (p *SpotifyProvider) Authenticate() error {
	p.mu.Lock()
	if p.session != nil {
		p.mu.Unlock()
		return nil
	}
	clientID := p.clientID
	p.mu.Unlock()

	if clientID == "" {
		return fmt.Errorf("spotify: no client ID available")
	}
	sess, err := NewSession(context.Background(), clientID)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.session = sess
	p.mu.Unlock()
	return nil
}

// Close releases the session if one was created.
func (p *SpotifyProvider) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.session != nil {
		p.session.Close()
		p.session = nil
	}
}

func (p *SpotifyProvider) Name() string { return "Spotify" }

// Playlists returns the authenticated user's Spotify playlists via the
// spclient protocol (works in all countries, bypasses api.spotify.com).
func (p *SpotifyProvider) Playlists() ([]playlist.PlaylistInfo, error) {
	if err := p.ensureSession(); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rootlist, err := p.session.SpRootlist(ctx)
	if err != nil {
		return nil, fmt.Errorf("spotify: list playlists: %w", err)
	}

	contents := rootlist.GetContents()
	if contents == nil {
		return nil, fmt.Errorf("spotify: empty rootlist")
	}

	// Collect playlist URIs from the rootlist. Filter to only actual playlists
	// (skip folders, collections, etc.).
	type plEntry struct {
		uri    string
		base62 string
	}
	var entries []plEntry
	for _, item := range contents.GetItems() {
		uri := item.GetUri()
		if !strings.HasPrefix(uri, "spotify:playlist:") {
			continue
		}
		base62 := strings.TrimPrefix(uri, "spotify:playlist:")
		entries = append(entries, plEntry{uri: uri, base62: base62})
	}

	// Fetch each playlist's name concurrently via spclient.
	type nameResult struct {
		idx   int
		name  string
		count int
	}
	results := make(chan nameResult, len(entries))
	workers := min(len(entries), 8)
	work := make(chan int, len(entries))
	for i := range entries {
		work <- i
	}
	close(work)

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range work {
				meta, err := p.session.SpPlaylistMetadata(ctx, entries[i].base62)
				if err != nil {
					results <- nameResult{idx: i, name: entries[i].base62}
					continue
				}
				name := entries[i].base62
				if attrs := meta.GetAttributes(); attrs != nil {
					if n := attrs.GetName(); n != "" {
						name = n
					}
				}
				count := int(meta.GetLength())
				results <- nameResult{idx: i, name: name, count: count}
			}
		}()
	}
	wg.Wait()
	close(results)

	// Collect results in order.
	nameMap := make(map[int]nameResult, len(entries))
	for r := range results {
		nameMap[r.idx] = r
	}

	all := make([]playlist.PlaylistInfo, 0, len(entries))
	for i, e := range entries {
		r := nameMap[i]
		all = append(all, playlist.PlaylistInfo{
			ID:         e.base62,
			Name:       r.name,
			TrackCount: r.count,
		})
	}

	return all, nil
}

// Tracks returns all tracks for the given Spotify playlist ID via the
// spclient protocol. Track.Path is set to a spotify:track:<id> URI for
// the player to resolve.
func (p *SpotifyProvider) Tracks(playlistID string) ([]playlist.Track, error) {
	if err := p.ensureSession(); err != nil {
		return nil, err
	}
	// Check cache.
	p.mu.Lock()
	if cached, ok := p.trackCache[playlistID]; ok && cached.tracks != nil {
		tracks := cached.tracks
		p.mu.Unlock()
		return tracks, nil
	}
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Resolve the playlist to get track URIs.
	uri := "spotify:playlist:" + playlistID
	spotCtx, err := p.session.SpContextResolve(ctx, uri)
	if err != nil {
		return nil, fmt.Errorf("spotify: list tracks: %w", err)
	}

	// Collect track URIs from the context pages.
	var trackURIs []string
	for _, page := range spotCtx.GetPages() {
		for _, track := range page.GetTracks() {
			trackURI := track.GetUri()
			if strings.HasPrefix(trackURI, "spotify:track:") {
				trackURIs = append(trackURIs, trackURI)
			}
		}
	}

	// Fetch metadata for each track concurrently via spclient.
	type trackResult struct {
		idx   int
		track playlist.Track
	}
	results := make(chan trackResult, len(trackURIs))
	workers := min(len(trackURIs), 8)
	work := make(chan int, len(trackURIs))
	for i := range trackURIs {
		work <- i
	}
	close(work)

	var wg sync.WaitGroup
	wg.Add(workers)
	for range workers {
		go func() {
			defer wg.Done()
			for i := range work {
				spotID, err := librespot.SpotifyIdFromUri(trackURIs[i])
				if err != nil {
					results <- trackResult{idx: i, track: playlist.Track{Path: trackURIs[i]}}
					continue
				}

				meta, err := p.session.SpTrackMetadata(ctx, *spotID)
				if err != nil {
					results <- trackResult{idx: i, track: playlist.Track{Path: trackURIs[i]}}
					continue
				}

				var artistName string
				if artists := meta.GetArtist(); len(artists) > 0 {
					artistName = artists[0].GetName()
				}
				var albumName string
				if album := meta.GetAlbum(); album != nil {
					albumName = album.GetName()
				}
				var durSecs int
				if d := meta.GetDuration(); d > 0 {
					durSecs = int(d) / 1000
				}

				results <- trackResult{idx: i, track: playlist.Track{
					Path:         trackURIs[i],
					Title:        meta.GetName(),
					Artist:       artistName,
					Album:        albumName,
					DurationSecs: durSecs,
				}}
			}
		}()
	}
	wg.Wait()
	close(results)

	// Collect results in order.
	indexed := make(map[int]trackResult, len(trackURIs))
	for r := range results {
		indexed[r.idx] = r
	}
	all := make([]playlist.Track, 0, len(trackURIs))
	for i := range trackURIs {
		all = append(all, indexed[i].track)
	}

	// Cache the fetched tracks.
	p.mu.Lock()
	if cached, ok := p.trackCache[playlistID]; ok {
		cached.tracks = all
	} else {
		p.trackCache[playlistID] = &playlistCache{tracks: all}
	}
	p.mu.Unlock()

	return all, nil
}

// isAuthError returns true if the error is an authentication/session-related
// failure that can be resolved by re-authenticating.
func isAuthError(err error) bool {
	var keyErr *audio.KeyProviderError
	if errors.As(err, &keyErr) {
		return true
	}
	// Catch wrapped context errors from a dead session.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	return false
}

// NewStreamer creates a SpotifyStreamer for the given spotify:track:xxx URI.
// Called by the player's StreamerFactory when it encounters a Spotify URI.
//
// If the stream fails due to an auth error (e.g. expired session, AES key
// rejection), the session is torn down, credentials are cleared, and a fresh
// interactive OAuth2 flow is triggered automatically. The stream is then
// retried once with the new session.
func (p *SpotifyProvider) NewStreamer(uri string) (beep.StreamSeekCloser, beep.Format, time.Duration, error) {
	if err := p.ensureSession(); err != nil {
		return nil, beep.Format{}, 0, err
	}
	spotID, err := librespot.SpotifyIdFromUri(uri)
	if err != nil {
		return nil, beep.Format{}, 0, fmt.Errorf("spotify: invalid URI %q: %w", uri, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	stream, err := p.session.NewStream(ctx, *spotID, 320) // TODO: make bitrate configurable via config.toml
	if err != nil {
		if !isAuthError(err) {
			return nil, beep.Format{}, 0, fmt.Errorf("spotify: new stream: %w", err)
		}

		// Auth error — attempt re-authentication and retry once.
		fmt.Fprintf(os.Stderr, "spotify: stream auth error (%v), attempting re-auth...\n", err)

		reconnCtx, reconnCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer reconnCancel()

		if reconnErr := p.session.Reconnect(reconnCtx); reconnErr != nil {
			return nil, beep.Format{}, 0, fmt.Errorf("spotify: re-auth failed: %w (original: %v)", reconnErr, err)
		}

		// Retry with the fresh session.
		retryCtx, retryCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer retryCancel()

		stream, err = p.session.NewStream(retryCtx, *spotID, 320)
		if err != nil {
			return nil, beep.Format{}, 0, fmt.Errorf("spotify: new stream after re-auth: %w", err)
		}
	}

	streamer := NewSpotifyStreamer(stream)
	return streamer, streamer.Format(), streamer.Duration(), nil
}
