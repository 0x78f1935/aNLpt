// Package scan looks through the folders a member chose to share and lists the video
// files in them.
//
// It is also what keeps this program from ever sending a file its owner did not choose to
// share. The archive never names a path: it asks for a file by an id, the id is only
// meaningful in the index built here, and the index only ever holds files that were found
// by walking a shared folder. There is no way to ask for anything else.
package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Extensions are the containers the archive passes on. Anything else is not listed, so
// subtitles, pictures and whatever else sits next to a video never leave the computer.
var Extensions = map[string]bool{
	".mp4": true, ".m4v": true, ".mkv": true, ".avi": true, ".mov": true, ".wmv": true,
	".mpg": true, ".mpeg": true, ".ts": true, ".webm": true, ".flv": true, ".vob": true,
	".ogv": true, ".m2ts": true,
}

// File is one video file in a shared folder.
type File struct {
	ID      string // stable for as long as the file stays where it is
	Folder  string // the id of the shared folder it was found in
	Path    string // from the shared folder down, with forward slashes
	Abs     string // where it really is; never sent anywhere
	Size    int64
	ModTime int64 // seconds since 1970
}

// Index is every file that may be sent, by id.
type Index struct {
	mu    sync.RWMutex
	files map[string]File
}

// NewIndex returns an empty index.
func NewIndex() *Index { return &Index{files: map[string]File{}} }

// Replace swaps in what one scan of one folder found.
func (ix *Index) Replace(folderID string, found []File) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for id, file := range ix.files {
		if file.Folder == folderID {
			delete(ix.files, id)
		}
	}
	for _, file := range found {
		ix.files[file.ID] = file
	}
}

// Drop forgets a folder that is no longer shared.
func (ix *Index) Drop(folderID string) { ix.Replace(folderID, nil) }

// Get finds a file by the id the archive asks for it by.
func (ix *Index) Get(id string) (File, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	file, ok := ix.files[id]
	return file, ok
}

// InFolder lists what one shared folder holds, in the order a file manager would.
func (ix *Index) InFolder(folderID string) []File {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	var found []File
	for _, file := range ix.files {
		if file.Folder == folderID {
			found = append(found, file)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Path < found[j].Path })
	return found
}

// Count says how many files are on offer, and how many bytes.
func (ix *Index) Count() (files int, bytes int64) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	for _, file := range ix.files {
		files++
		bytes += file.Size
	}
	return files, bytes
}

// Folder walks one shared folder.
//
// Hidden files and directories are skipped, and so is anything a symbolic link leads to
// outside the folder: a link called "films" pointing at somebody's whole home directory
// would otherwise share all of it.
func Folder(folderID, root string) ([]File, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}

	var found []File
	err = filepath.WalkDir(realRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A directory that cannot be read is skipped, not a reason to give up on the rest.
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if path != realRoot && hidden(path, name) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !Extensions[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		// WalkDir does not follow links to directories. A link to a file is followed
		// only when the file it leads to is inside the shared folder too.
		real := path
		if entry.Type()&fs.ModeSymlink != 0 {
			real, err = filepath.EvalSymlinks(path)
			if err != nil || !inside(realRoot, real) {
				return nil
			}
		}
		info, err := os.Stat(real)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return nil
		}
		relative, err := filepath.Rel(realRoot, path)
		if err != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		found = append(found, File{
			ID:      fileID(folderID, relative),
			Folder:  folderID,
			Path:    relative,
			Abs:     real,
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
		})
		return nil
	})
	return found, err
}

func inside(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// fileID is made from where the file is, not from what is in it: hashing a terabyte of
// video to name it would take a night. A file that moves is a new file, which is right.
func fileID(folderID, relative string) string {
	sum := sha256.Sum256([]byte(folderID + "\x00" + relative))
	return hex.EncodeToString(sum[:16])
}
