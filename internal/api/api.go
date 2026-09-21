// Package api is everything aNLpt says to the archive, and all of it.
//
// It is kept in one small file on purpose. The source of this program is public so that
// anybody can check what it sends, and this is where to look: the name of a shared folder
// (never where it is), the names and sizes of the video files inside it, and, when
// another member asks for one, the file itself. Nothing else leaves the computer.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/0x78f1935/aNLpt/internal/auth"
)

// ErrRevoked means whoever runs the archive switched this client off.
var ErrRevoked = errors.New("this client was switched off by the archive")

// ErrOver means a transfer has ended, however it ended. Stop sending.
var ErrOver = errors.New("the transfer is over")

// ErrNotYet means a piece was sent before the archive wanted it.
var ErrNotYet = errors.New("that piece is not wanted yet")

// Client talks to one archive as one member.
type Client struct {
	Server    string
	UserAgent string
	Session   *auth.Session
	HTTP      *http.Client
	// ID is the archive's id for this installation, known once Register has run.
	ID string
}

// Problem is the archive saying no in its own words.
type Problem struct {
	Status  int
	Code    string
	Message string
}

func (p *Problem) Error() string {
	return fmt.Sprintf("the archive answered %d (%s): %s", p.Status, p.Code, p.Message)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, header http.Header, out any) error {
	for attempt := 0; ; attempt++ {
		token, err := c.Session.Token(ctx)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.Server+"/api/v1"+path, body)
		if err != nil {
			return err
		}
		for key, values := range header {
			req.Header[key] = values
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		// The archive's front door turns away programs that do not say who they are.
		req.Header.Set("User-Agent", c.UserAgent)

		resp, err := c.HTTP.Do(req)
		if err != nil {
			return fmt.Errorf("reaching the archive: %w", err)
		}
		// A token the archive no longer takes is renewed once, if the body can be sent again.
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			c.Session.Forget()
			if seeker, ok := body.(io.Seeker); ok {
				if _, err := seeker.Seek(0, io.SeekStart); err != nil {
					return err
				}
				continue
			}
			if body == nil {
				continue
			}
			return auth.ErrSignedOut
		}
		defer resp.Body.Close()
		return read(resp, out)
	}
}

func read(resp *http.Response, out any) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out == nil || resp.StatusCode == http.StatusNoContent {
			_, _ = io.Copy(io.Discard, resp.Body)
			return nil
		}
		return json.NewDecoder(resp.Body).Decode(out)
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	_ = json.Unmarshal(raw, &envelope)
	problem := &Problem{Status: resp.StatusCode, Code: envelope.Error.Code, Message: envelope.Error.Message}
	switch {
	case problem.Code == "client_revoked":
		return ErrRevoked
	case resp.StatusCode == http.StatusGone:
		return ErrOver
	case resp.StatusCode == http.StatusConflict:
		return ErrNotYet
	case resp.StatusCode == http.StatusUnauthorized:
		return auth.ErrSignedOut
	}
	return problem
}

func (c *Client) json(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	header := http.Header{}
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
		header.Set("Content-Type", "application/json")
	}
	return c.do(ctx, method, path, body, header, out)
}

// Register says hello. The same device saying it twice is the same client.
func (c *Client) Register(ctx context.Context, deviceID, name, platform, version string) error {
	var answer struct {
		PublicID string `json:"public_id"`
	}
	err := c.json(ctx, http.MethodPost, "/tracker/client/register/", map[string]string{
		"device_id": deviceID, "name": name, "platform": platform, "app_version": version,
	}, &answer)
	if err != nil {
		return err
	}
	c.ID = answer.PublicID
	return nil
}

// Member is what the archive says about who is signed in.
type Member struct {
	Username       string     `json:"username"`
	DisplayName    string     `json:"display_name"`
	Level          int        `json:"level"`
	XP             int        `json:"xp"`
	MayDownload    bool       `json:"may_download"`
	IsSharing      bool       `json:"is_sharing"`
	DownloadEndsAt *time.Time `json:"download_ends_at"`
	// PreferredLanguage is the language the member reads the archive in: nl or en.
	PreferredLanguage string `json:"preferred_language"`
	// ShowHolderName is what the member chose on their profile about their name next to
	// their copies. A folder shared here starts out the same way.
	ShowHolderName bool `json:"show_holder_name"`
}

// Me asks how the member stands: level, XP, and whether they may download.
func (c *Client) Me(ctx context.Context) (Member, error) {
	var member Member
	err := c.json(ctx, http.MethodGet, "/tracker/client/"+c.ID+"/me/", nil, &member)
	return member, err
}

// FolderSettings is how a folder is shared. Its name, and never where it is.
type FolderSettings struct {
	Label      string `json:"label"`
	Visibility string `json:"visibility"`
	Anonymous  bool   `json:"anonymous"`
	Language   string `json:"language"`
	// OtherLanguages are further spoken tracks on the same files.
	OtherLanguages []string `json:"other_languages"`
	Downloadable   bool     `json:"downloadable"`
}

// PutFolder shares a folder, or changes how one is shared.
func (c *Client) PutFolder(ctx context.Context, folderID string, settings FolderSettings) error {
	return c.json(ctx, http.MethodPut, "/tracker/client/"+c.ID+"/folders/"+folderID+"/", settings, nil)
}

// FolderIDs asks which folders the archive thinks this client shares.
func (c *Client) FolderIDs(ctx context.Context) ([]string, error) {
	var found []struct {
		ID string `json:"client_folder_id"`
	}
	if err := c.json(ctx, http.MethodGet, "/tracker/client/"+c.ID+"/folders/", nil, &found); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(found))
	for _, folder := range found {
		ids = append(ids, folder.ID)
	}
	return ids, nil
}

// DeleteFolder stops sharing a folder.
func (c *Client) DeleteFolder(ctx context.Context, folderID string) error {
	err := c.json(ctx, http.MethodDelete, "/tracker/client/"+c.ID+"/folders/"+folderID+"/", nil, nil)
	var problem *Problem
	if errors.As(err, &problem) && problem.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// ReportedFile is one file as the archive is told about it.
type ReportedFile struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Audience is who may fetch this file where that is not what its folder says.
	Audience string `json:"audience,omitempty"`
	// Language and OtherLanguages are what its series is spoken in, where that is not
	// what its folder says.
	Language       string   `json:"language,omitempty"`
	OtherLanguages []string `json:"other_languages,omitempty"`
	MTime          int64    `json:"mtime"`
}

// Report sends one piece of what a folder holds. done goes on the last one.
func (c *Client) Report(ctx context.Context, folderID string, generation int64, files []ReportedFile, done bool) error {
	if files == nil {
		files = []ReportedFile{}
	}
	return c.json(ctx, http.MethodPut, "/tracker/client/"+c.ID+"/folders/"+folderID+"/report/",
		map[string]any{"generation": generation, "files": files, "done": done}, nil)
}

// Title is a title in the archive's catalogue.
type Title struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Year *int   `json:"year"`
}

// Directory is how the archive read one directory of a shared folder.
type Directory struct {
	ID     int    `json:"id"`
	Folder string `json:"folder"`
	Name   string `json:"name"`
	// Search is the name cleaned up for a search box: no year, no release group.
	Search      string   `json:"search"`
	MatchState  string   `json:"match_state"`
	Title       *Title   `json:"title"`
	Files       int      `json:"files"`
	Episodes    int      `json:"episodes"`
	Suggestions []Title  `json:"suggestions"`
	Unread      []string `json:"unread"`
}

// Directories asks what was recognised and what was not.
func (c *Client) Directories(ctx context.Context) ([]Directory, error) {
	var found []Directory
	err := c.json(ctx, http.MethodGet, "/tracker/client/"+c.ID+"/directories/", nil, &found)
	return found, err
}

// Wanted is a file somebody asked for. Who asked is not said, and not needed.
type Wanted struct {
	ID        string `json:"id"`
	File      string `json:"file"`
	Size      int64  `json:"size"`
	MTime     int64  `json:"mtime"`
	PieceSize int64  `json:"piece_size"`
	Pieces    int    `json:"pieces"`
	// SendUntil is the first piece not to send yet.
	SendUntil int `json:"send_until"`
	// SendRate is the fastest the archive wants this sent, in bytes a second. It is
	// decided over there so it is the same for everybody and can change without a new
	// version of this program. Zero is no limit.
	SendRate int `json:"send_rate"`
}

// Poll asks whether anybody wants a file. It is also how the archive knows this
// computer is on.
func (c *Client) Poll(ctx context.Context) (wanted []Wanted, askAgainIn time.Duration, err error) {
	var answer struct {
		Wanted     []Wanted `json:"wanted"`
		AskAgainIn int      `json:"ask_again_in"`
	}
	if err := c.json(ctx, http.MethodGet, "/tracker/client/"+c.ID+"/poll/", nil, &answer); err != nil {
		return nil, 0, err
	}
	return answer.Wanted, time.Duration(answer.AskAgainIn) * time.Second, nil
}

// Pause tells the archive that sharing was paused or taken up again, so the download
// buttons on the site go at once rather than after half a minute of silence.
func (c *Client) Pause(ctx context.Context, paused bool) error {
	return c.json(ctx, http.MethodPost, "/tracker/client/"+c.ID+"/pause/", map[string]bool{"paused": paused}, nil)
}

// SendPiece sends one piece of a file, with its checksum. It returns how far the archive
// is willing to take pieces now.
func (c *Client) SendPiece(ctx context.Context, transferID string, index int, piece []byte, sha256hex string) (sendUntil int, err error) {
	header := http.Header{}
	header.Set("Content-Type", "application/octet-stream")
	header.Set("X-Piece-Sha256", sha256hex)
	var answer struct {
		SendUntil int `json:"send_until"`
	}
	path := "/tracker/relay/" + c.ID + "/" + transferID + "/pieces/" + strconv.Itoa(index) + "/"
	err = c.do(ctx, http.MethodPut, path, bytes.NewReader(piece), header, &answer)
	return answer.SendUntil, err
}

// GiveUp tells the archive a file cannot be sent after all, so whoever is waiting for it
// hears at once instead of after a minute of silence.
func (c *Client) GiveUp(ctx context.Context, transferID, reason string) error {
	return c.json(ctx, http.MethodPost, "/tracker/relay/"+c.ID+"/"+transferID+"/give-up/",
		map[string]string{"reason": reason}, nil)
}
