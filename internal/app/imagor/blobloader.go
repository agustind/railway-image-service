package imagor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cshum/imagor"
	"github.com/cshum/imagor/imagorpath"
	"github.com/jaredLunde/railway-image-service/internal/app/keyval"
)

var dotFileRegex = regexp.MustCompile(`/.`)

// BlobStorage File Storage implements imagor.Storage interface
type BlobStorage struct {
	KV              *keyval.KeyVal
	PathPrefix      string
	Blacklists      []*regexp.Regexp
	MkdirPermission os.FileMode
	WritePermission os.FileMode
	SaveErrIfExists bool
	SafeChars       string

	safeChars imagorpath.SafeChars
}

// New creates FileStorage
func NewBlobStorage(kv *keyval.KeyVal, uploadPath string) *BlobStorage {
	s := &BlobStorage{
		KV:              kv,
		Blacklists:      []*regexp.Regexp{dotFileRegex},
		MkdirPermission: 0755,
		WritePermission: 0666,
		PathPrefix:      uploadPath,
	}
	s.safeChars = imagorpath.NewSafeChars(s.SafeChars)
	return s
}

// Path transforms and validates image key for storage path
func (s *BlobStorage) Path(image string) (string, bool) {
	key := []byte(image)
	if strings.HasPrefix(image, "/") {
		key = []byte(image[1:])
	}
	if !bytes.HasPrefix(key, []byte("blob/")) {
		return "", false
	}
	key = bytes.TrimPrefix(key, []byte("blob/"))
	if s.KV.GetRecord(key).Deleted != keyval.NO {
		return "", false
	}
	return filepath.Join(s.PathPrefix, keyval.KeyToPath(key)), true
}

// Get implements imagor.Storage interface
func (s *BlobStorage) Get(_ *http.Request, image string) (*imagor.Blob, error) {
	image, ok := s.Path(image)
	if !ok {
		return nil, imagor.ErrInvalid
	}
	f := imagor.NewBlobFromFile(image, func(stat os.FileInfo) error {
		return nil
	})
	return f, nil
}

// Put implements imagor.Storage interface
func (s *BlobStorage) Put(_ context.Context, image string, blob *imagor.Blob) (err error) {
	image, ok := s.Path(image)
	if !ok {
		return imagor.ErrInvalid
	}
	if err = os.MkdirAll(filepath.Dir(image), s.MkdirPermission); err != nil {
		return
	}
	reader, _, err := blob.NewReader()
	if err != nil {
		return err
	}
	defer func() {
		_ = reader.Close()
	}()
	flag := os.O_RDWR | os.O_CREATE | os.O_TRUNC
	if s.SaveErrIfExists {
		flag = os.O_RDWR | os.O_CREATE | os.O_EXCL
	}
	w, err := os.OpenFile(image, flag, s.WritePermission)
	if err != nil {
		return
	}
	defer func() {
		_ = w.Close()
		if err != nil {
			_ = os.Remove(w.Name())
		}
	}()
	if _, err = io.Copy(w, reader); err != nil {
		return
	}
	if err = w.Sync(); err != nil {
		return
	}
	return
}

// Delete implements imagor.Storage interface
func (s *BlobStorage) Delete(_ context.Context, image string) error {
	image, ok := s.Path(image)
	if !ok {
		return imagor.ErrInvalid
	}
	return os.Remove(image)
}

// Stat implements imagor.Storage interface
func (s *BlobStorage) Stat(_ context.Context, image string) (stat *imagor.Stat, err error) {
	image, ok := s.Path(image)
	if !ok {
		return nil, imagor.ErrInvalid
	}
	osStat, err := os.Stat(image)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, imagor.ErrNotFound
		}
		return nil, err
	}
	size := osStat.Size()
	modTime := osStat.ModTime()
	return &imagor.Stat{
		Size:         size,
		ModifiedTime: modTime,
	}, nil
}
