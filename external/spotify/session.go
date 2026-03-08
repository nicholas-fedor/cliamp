package spotify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"cliamp/internal/appdir"
	"cliamp/internal/browser"

	librespot "github.com/devgianlu/go-librespot"
	librespotPlayer "github.com/devgianlu/go-librespot/player"
	connectpb "github.com/devgianlu/go-librespot/proto/spotify/connectstate"
	devicespb "github.com/devgianlu/go-librespot/proto/spotify/connectstate/devices"
	extmetadatapb "github.com/devgianlu/go-librespot/proto/spotify/extendedmetadata"
	metadatapb "github.com/devgianlu/go-librespot/proto/spotify/metadata"
	playlist4pb "github.com/devgianlu/go-librespot/proto/spotify/playlist4"
	"github.com/devgianlu/go-librespot/session"
	"github.com/devgianlu/go-librespot/spclient"
	"golang.org/x/oauth2"
	spotifyoauth2 "golang.org/x/oauth2/spotify"
	"google.golang.org/protobuf/proto"
)

// storedCreds holds persisted Spotify credentials for re-authentication.
type storedCreds struct {
	Username string `json:"username"`
	Data     []byte `json:"data"`
	DeviceID string `json:"device_id"`
}

// CallbackPort is the fixed port for the OAuth2 callback server.
// Must match the redirect URI registered in the Spotify Developer app.
const CallbackPort = 19872

// Session manages a go-librespot session and player for Spotify integration.
type Session struct {
	mu       sync.Mutex
	sess     *session.Session
	player   *librespotPlayer.Player
	devID    string
	clientID string // Spotify Developer app client ID
}

// NewSession creates a go-librespot session, using stored credentials if
// available, otherwise starting an interactive OAuth2 flow.
func NewSession(ctx context.Context, clientID string) (*Session, error) {
	creds, err := loadCreds()
	if err == nil && creds.Username != "" && len(creds.Data) > 0 {
		s, err := newSessionFromStored(ctx, clientID, creds)
		if err == nil {
			return s, nil
		}
		// Stored credentials failed (expired/revoked), fall through to interactive.
	}
	return newInteractiveSession(ctx, clientID)
}

// NewSessionSilent is like NewSession but only uses stored credentials.
// Returns an error if interactive auth is required.
func NewSessionSilent(ctx context.Context, clientID string) (*Session, error) {
	creds, err := loadCreds()
	if err != nil || creds.Username == "" || len(creds.Data) == 0 {
		return nil, fmt.Errorf("no stored credentials")
	}
	return newSessionFromStored(ctx, clientID, creds)
}

// newSessionFromStored creates a session from stored credentials.
func newSessionFromStored(ctx context.Context, clientID string, creds *storedCreds) (*Session, error) {
	devID := creds.DeviceID
	if devID == "" {
		devID = generateDeviceID()
	}

	sess, err := session.NewSessionFromOptions(ctx, &session.Options{
		Log:        &librespot.NullLogger{},
		DeviceType: devicespb.DeviceType_COMPUTER,
		DeviceId:   devID,
		Credentials: session.StoredCredentials{
			Username: creds.Username,
			Data:     creds.Data,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("spotify: stored auth: %w", err)
	}

	s := &Session{sess: sess, devID: devID, clientID: clientID}

	// Re-save credentials (device ID may have been generated).
	if err := saveCreds(&storedCreds{
		Username: sess.Username(),
		Data:     sess.StoredCredentials(),
		DeviceID: devID,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "spotify: failed to save credentials: %v\n", err)
	}

	if err := s.initPlayer(); err != nil {
		sess.Close()
		return nil, err
	}
	return s, nil
}

// oauthScopes are the Spotify OAuth2 scopes needed for authentication and
// streaming via go-librespot's spclient protocol.
var oauthScopes = []string{
	"streaming",
	"playlist-read-collaborative",
	"playlist-read-private",
	"user-read-private",
}

// spotifyOAuthConfig returns the OAuth2 config for the given client ID.
func spotifyOAuthConfig(clientID string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:    clientID,
		RedirectURL: fmt.Sprintf("http://127.0.0.1:%d/login", CallbackPort),
		Scopes:      oauthScopes,
		Endpoint:    spotifyoauth2.Endpoint,
	}
}

// oauthCallbackHTML is the response sent to the browser after a successful OAuth2 callback.
const oauthCallbackHTML = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>cliamp</title></head>
<body style="font-family:system-ui;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:#1a1a2e;color:#e0e0e0">
<div style="text-align:center">
<h2>✅ Authenticated!</h2>
<p>You can close this tab now.</p>
<script>setTimeout(function(){window.close()},1500)</script>
</div></body></html>`

// performOAuth2PKCE runs an OAuth2 PKCE flow: opens a browser for user consent,
// waits for the callback, and exchanges the code for a token.
func performOAuth2PKCE(clientID string) (*oauth2.Token, error) {
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", CallbackPort))
	if err != nil {
		return nil, fmt.Errorf("listen on port %d: %w", CallbackPort, err)
	}

	oauthConf := spotifyOAuthConfig(clientID)

	verifier := oauth2.GenerateVerifier()
	authURL := oauthConf.AuthCodeURL("", oauth2.S256ChallengeOption(verifier))

	codeCh := make(chan string, 1)
	go func() {
		if err := http.Serve(lis, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			code := r.URL.Query().Get("code")
			if code != "" {
				codeCh <- code
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(oauthCallbackHTML))
		})); err != nil && !errors.Is(err, net.ErrClosed) {
			fmt.Fprintf(os.Stderr, "spotify: auth callback server error: %v\n", err)
		}
	}()

	_ = browser.Open(authURL)

	code := <-codeCh
	_ = lis.Close()

	token, err := oauthConf.Exchange(context.Background(), code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("token exchange: %w", err)
	}

	return token, nil
}

func newInteractiveSession(ctx context.Context, clientID string) (*Session, error) {
	devID := generateDeviceID()

	token, err := performOAuth2PKCE(clientID)
	if err != nil {
		return nil, fmt.Errorf("spotify: %w", err)
	}

	username, _ := token.Extra("username").(string)
	accessToken := token.AccessToken

	// Create go-librespot session using the OAuth2 token.
	sess, err := session.NewSessionFromOptions(ctx, &session.Options{
		Log:        &librespot.NullLogger{},
		DeviceType: devicespb.DeviceType_COMPUTER,
		DeviceId:   devID,
		Credentials: session.SpotifyTokenCredentials{
			Username: username,
			Token:    accessToken,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("spotify: session from token: %w", err)
	}

	// Persist stored credentials for future sessions.
	if err := saveCreds(&storedCreds{
		Username: sess.Username(),
		Data:     sess.StoredCredentials(),
		DeviceID: devID,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "spotify: failed to save credentials: %v\n", err)
	}

	s := &Session{sess: sess, devID: devID, clientID: clientID}
	if err := s.initPlayer(); err != nil {
		sess.Close()
		return nil, err
	}
	return s, nil
}

// initPlayer creates the go-librespot player. We only use NewStream() for
// decoded AudioSources — audio output is routed through cliamp's Beep pipeline,
// not go-librespot's output backend.
func (s *Session) initPlayer() error {
	// go-librespot uses this for media restriction checks but Premium
	// accounts can play all tracks regardless.
	countryCode := "US"
	p, err := librespotPlayer.NewPlayer(&librespotPlayer.Options{
		Spclient:             s.sess.Spclient(),
		AudioKey:             s.sess.AudioKey(),
		Events:               s.sess.Events(),
		Log:                  &librespot.NullLogger{},
		CountryCode:          &countryCode,
		NormalisationEnabled: true,
		AudioBackend:         "pipe",
		AudioOutputPipe:      os.DevNull,
	})
	if err != nil {
		return fmt.Errorf("spotify: player init: %w", err)
	}
	s.player = p
	return nil
}

// NewStream creates a decoded audio stream for the given Spotify track ID.
func (s *Session) NewStream(ctx context.Context, spotID librespot.SpotifyId, bitrate int) (*librespotPlayer.Stream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.player.NewStream(ctx, http.DefaultClient, spotID, bitrate, 0)
}

// Username returns the authenticated Spotify username.
func (s *Session) Username() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sess.Username()
}

// Spclient returns the underlying go-librespot spclient.
func (s *Session) Spclient() *spclient.Spclient {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sess.Spclient()
}

// SpRootlist fetches the user's playlist collection via spclient (bypasses
// api.spotify.com geo-restrictions).
func (s *Session) SpRootlist(ctx context.Context) (*playlist4pb.SelectedListContent, error) {
	username := s.Username()
	sp := s.Spclient()

	resp, err := sp.Request(ctx, "GET",
		fmt.Sprintf("/playlist/v2/user/%s/rootlist", url.PathEscape(username)),
		nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("spclient rootlist: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("spclient rootlist: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("spclient rootlist: read body: %w", err)
	}

	var list playlist4pb.SelectedListContent
	if err := proto.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("spclient rootlist: unmarshal: %w", err)
	}
	return &list, nil
}

// SpPlaylistMetadata fetches a single playlist's attributes via spclient.
func (s *Session) SpPlaylistMetadata(ctx context.Context, base62ID string) (*playlist4pb.SelectedListContent, error) {
	sp := s.Spclient()

	resp, err := sp.Request(ctx, "GET",
		fmt.Sprintf("/playlist/v2/playlist/%s", base62ID),
		nil, nil, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("spclient playlist metadata: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var list playlist4pb.SelectedListContent
	if err := proto.Unmarshal(body, &list); err != nil {
		return nil, err
	}
	return &list, nil
}

// SpContextResolve resolves a playlist/album context into pages of tracks
// via the spclient (bypasses api.spotify.com).
func (s *Session) SpContextResolve(ctx context.Context, uri string) (*connectpb.Context, error) {
	sp := s.Spclient()
	return sp.ContextResolve(ctx, uri)
}

// SpTrackMetadata fetches full metadata for a single track via the spclient
// extended metadata endpoint (TRACK_V4).
func (s *Session) SpTrackMetadata(ctx context.Context, spotID librespot.SpotifyId) (*metadatapb.Track, error) {
	sp := s.Spclient()
	var track metadatapb.Track
	if err := sp.ExtendedMetadataSimple(ctx, spotID, extmetadatapb.ExtensionKind_TRACK_V4, &track); err != nil {
		return nil, err
	}
	return &track, nil
}

// Close releases all session and player resources.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.player != nil {
		s.player.Close()
	}
	if s.sess != nil {
		s.sess.Close()
	}
}

// Reconnect tears down the current session, clears stored credentials, and
// re-authenticates interactively. This is called automatically when playback
// encounters an auth-related error (e.g. AES key retrieval failure) so the
// user doesn't get stuck in an error loop.
//
// The new session is established before tearing down the old one to avoid a
// window where s.sess/s.player are nil (which would crash concurrent callers
// like NewStream).
func (s *Session) Reconnect(ctx context.Context) error {
	// Capture clientID without holding the lock during the (potentially long)
	// interactive OAuth2 flow.
	s.mu.Lock()
	clientID := s.clientID
	s.mu.Unlock()

	// Clear stored credentials so we don't reuse stale ones.
	if err := deleteCreds(); err != nil {
		fmt.Fprintf(os.Stderr, "spotify: failed to clear stored credentials: %v\n", err)
	}

	// Create the new session outside the lock — this may open a browser and
	// block for user interaction.
	newSess, err := NewSession(ctx, clientID)
	if err != nil {
		return fmt.Errorf("spotify: reconnect: %w", err)
	}

	// Now acquire the lock and atomically swap internals.
	s.mu.Lock()
	oldPlayer := s.player
	oldSess := s.sess
	s.sess = newSess.sess
	s.player = newSess.player
	s.devID = newSess.devID
	s.mu.Unlock()

	// Tear down old session/player after the swap so there's no nil window.
	if oldPlayer != nil {
		oldPlayer.Close()
	}
	if oldSess != nil {
		oldSess.Close()
	}

	// Prevent newSess.Close() from tearing down the resources we just adopted.
	newSess.mu.Lock()
	newSess.sess = nil
	newSess.player = nil
	newSess.mu.Unlock()

	fmt.Fprintf(os.Stderr, "spotify: re-authenticated successfully\n")
	return nil
}

// deleteCreds removes the stored credentials file.
func deleteCreds() error {
	path, err := credsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func credsPath() (string, error) {
	dir, err := appdir.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "spotify_credentials.json"), nil
}

func generateDeviceID() string {
	b := make([]byte, 20)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func loadCreds() (*storedCreds, error) {
	path, err := credsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var creds storedCreds
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}

func saveCreds(creds *storedCreds) error {
	path, err := credsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

